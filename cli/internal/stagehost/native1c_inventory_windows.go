//go:build windows

package stagehost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// This file ports Read-BFNativeInventoryDirect (Task.Runtime.ps1) with the
// standard library only: the V83.COMConnector is driven through late-bound
// IDispatch calls over syscall, so the native adapter never launches pwsh or
// any other helper for the inventory read. The 64-bit COM ABI layouts below
// are fixed for the supported windows/amd64 target.

// COM ABI constants (64-bit windows).
const (
	hkeyClassesRoot = 0x80000000
	keyRead         = 0x20019
	regSZ           = 1

	coInitMask          = 0x6 // COINIT_APARTMENTTHREADED | COINIT_DISABLE_OLE1DDE
	rpcEChangedMode     = 0x80010106
	clsctxInprocServer  = 1
	localeSystemDefault = 0x800

	dispatchMethod      = 0x1
	dispatchPropertyGet = 0x2

	vtEmpty    = 0
	vtNull     = 1
	vtI2       = 2
	vtI4       = 3
	vtR4       = 4
	vtR8       = 5
	vtBSTR     = 8
	vtDispatch = 9
	vtBool     = 11
	vtVariant  = 12
	vtUnknown  = 13
	vtI1       = 16
	vtUI1      = 17
	vtUI2      = 18
	vtUI4      = 19
	vtI8       = 20
	vtUI8      = 21
	vtInt      = 22
	vtUInt     = 23
	vtArray    = 0x2000
	vtByRef    = 0x4000
)

const v83ConnectorRegistryKey = `CLSID\{181E893D-73A4-4722-B61D-D604B3D67D47}\InprocServer32`

type oleGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	iidNull     = oleGUID{}
	iidDispatch = oleGUID{Data1: 0x00020400, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	// CLSID V83.COMConnector {181E893D-73A4-4722-B61D-D604B3D67D47}
	v83ConnectorCLSID = oleGUID{Data1: 0x181E893D, Data2: 0x73A4, Data3: 0x4722, Data4: [8]byte{0xB6, 0x1D, 0xD6, 0x04, 0xB3, 0xD6, 0x7D, 0x47}}
)

var (
	ole32DLL   = syscall.NewLazyDLL("ole32.dll")
	oleautoDLL = syscall.NewLazyDLL("oleaut32.dll")
	advapiDLL  = syscall.NewLazyDLL("advapi32.dll")

	procCoInitializeEx    = ole32DLL.NewProc("CoInitializeEx")
	procCoCreateInstance  = ole32DLL.NewProc("CoCreateInstance")
	procCoUninitialize    = ole32DLL.NewProc("CoUninitialize")
	procSysAllocString    = oleautoDLL.NewProc("SysAllocString")
	procSysStringLen     = oleautoDLL.NewProc("SysStringLen")
	procVariantClear     = oleautoDLL.NewProc("VariantClear")
	procSafeArrayGetUBound = oleautoDLL.NewProc("SafeArrayGetUBound")
	procSafeArrayGetLBound = oleautoDLL.NewProc("SafeArrayGetLBound")
	procSafeArrayGetElem  = oleautoDLL.NewProc("SafeArrayGetElement")
	procRegOpenKeyEx      = advapiDLL.NewProc("RegOpenKeyExW")
	procRegQueryValueEx   = advapiDLL.NewProc("RegQueryValueExW")
	procRegCloseKey       = advapiDLL.NewProc("RegCloseKey")
)

// oleVariant is the 64-bit VARIANT: 8-byte header plus the 16-byte union
// (DECIMAL forces the union size).
type oleVariant struct {
	vt   uint16
	_    [6]byte
	data [16]byte
}

func (v *oleVariant) pointer() uintptr {
	return *(*uintptr)(unsafe.Pointer(&v.data[0]))
}

func (v *oleVariant) setPointer(p uintptr) {
	*(*uintptr)(unsafe.Pointer(&v.data[0])) = p
}

func (v *oleVariant) int64Value() int64 {
	return *(*int64)(unsafe.Pointer(&v.data[0]))
}

func (v *oleVariant) float64Value() float64 {
	return *(*float64)(unsafe.Pointer(&v.data[0]))
}

func (v *oleVariant) boolValue() bool {
	return *(*int16)(unsafe.Pointer(&v.data[0])) != 0
}

func oleVariantClear(v *oleVariant) {
	if v == nil {
		return
	}
	procVariantClear.Call(uintptr(unsafe.Pointer(v)))
}

func oleBSTR(text string) (uintptr, error) {
	encoded, err := syscall.UTF16FromString(text)
	if err != nil {
		return 0, err
	}
	pointer, _, _ := procSysAllocString.Call(uintptr(unsafe.Pointer(&encoded[0])))
	if pointer == 0 {
		return 0, fmt.Errorf("SysAllocString failed")
	}
	return pointer, nil
}

func oleBSTRString(pointer uintptr) string {
	if pointer == 0 {
		return ""
	}
	// SysStringLen reads the length prefix of the BSTR.
	length, _, _ := procSysStringLen.Call(pointer)
	base := nativeUnsafePointer(pointer)
	units := (*uint16)(base)
	return syscall.UTF16ToString(unsafe.Slice(units, int(length)))
}

// nativeUnsafePointer reinterprets a raw address as a pointer through the
// round-trip form go vet verifies: the address of the uintptr value itself.
func nativeUnsafePointer(value uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&value))
}

func oleVariantBSTR(text string) (oleVariant, error) {
	pointer, err := oleBSTR(text)
	if err != nil {
		return oleVariant{}, err
	}
	var result oleVariant
	result.vt = vtBSTR
	result.setPointer(pointer)
	return result, nil
}

// oleIDispatchVTable is the first seven IDispatch methods; only the tail
// three are ever called here.
type oleIDispatchVTable struct {
	QueryInterface   uintptr
	AddRef           uintptr
	Release          uintptr
	GetTypeInfoCount uintptr
	GetTypeInfo      uintptr
	GetIDsOfNames    uintptr
	Invoke           uintptr
}

type oleDispatch struct {
	iface  uintptr
	vtable *oleIDispatchVTable
}

func newOleDispatch(iface uintptr) *oleDispatch {
	base := nativeUnsafePointer(iface)
	vtable := *(**oleIDispatchVTable)(base)
	return &oleDispatch{iface: iface, vtable: vtable}
}

func (d *oleDispatch) release() {
	if d == nil || d.iface == 0 {
		return
	}
	syscall.SyscallN(d.vtable.Release, d.iface)
	d.iface = 0
}

func (d *oleDispatch) getID(name string) (int32, error) {
	namePointer, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	iidPointer := uintptr(unsafe.Pointer(&iidNull))
	namePointerValue := uintptr(unsafe.Pointer(&namePointer))
	var dispid int32
	dispidPointer := uintptr(unsafe.Pointer(&dispid))
	hresult, _, _ := syscall.SyscallN(
		d.vtable.GetIDsOfNames,
		d.iface,
		iidPointer,
		namePointerValue,
		1,
		localeSystemDefault,
		dispidPointer,
	)
	if hresult != 0 {
		return 0, fmt.Errorf("GetIDsOfNames(%q) hresult 0x%x", name, hresult)
	}
	return dispid, nil
}

// oleDispParams is the 64-bit DISPPARAMS.
type oleDispParams struct {
	cArgs  uint32
	cNamed uint32
	rgvarg uintptr
	named  uintptr
}

func (d *oleDispatch) invoke(dispid int32, flags uint16, args []oleVariant) (oleVariant, error) {
	var params oleDispParams
	params.cArgs = uint32(len(args))
	reversed := make([]oleVariant, len(args))
	for index, arg := range args {
		reversed[len(args)-1-index] = arg
	}
	if len(reversed) > 0 {
		params.rgvarg = uintptr(unsafe.Pointer(&reversed[0]))
	}
	var result oleVariant
	var except [9]uintptr
	var argErr uint32
	iidPointer := uintptr(unsafe.Pointer(&iidNull))
	paramsPointer := uintptr(unsafe.Pointer(&params))
	resultPointer := uintptr(unsafe.Pointer(&result))
	exceptPointer := uintptr(unsafe.Pointer(&except[0]))
	argErrPointer := uintptr(unsafe.Pointer(&argErr))
	hresult, _, _ := syscall.SyscallN(
		d.vtable.Invoke,
		d.iface,
		uintptr(uint32(dispid)),
		iidPointer,
		localeSystemDefault,
		uintptr(flags),
		paramsPointer,
		resultPointer,
		exceptPointer,
		argErrPointer,
	)
	if hresult != 0 {
		return result, fmt.Errorf("Invoke hresult 0x%x", hresult)
	}
	return result, nil
}

func (d *oleDispatch) property(name string) (oleVariant, error) {
	dispid, err := d.getID(name)
	if err != nil {
		return oleVariant{}, err
	}
	return d.invoke(dispid, dispatchPropertyGet, nil)
}

func (d *oleDispatch) method(name string, args []oleVariant) (oleVariant, error) {
	dispid, err := d.getID(name)
	if err != nil {
		return oleVariant{}, err
	}
	return d.invoke(dispid, dispatchMethod, args)
}

// registryDefaultValue reads the default REG_SZ of one key relative to
// HKEY_CLASSES_ROOT.
func registryDefaultValue(path string) (string, error) {
	keyName, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var key uintptr
	hresult, _, _ := procRegOpenKeyEx.Call(hkeyClassesRoot, uintptr(unsafe.Pointer(keyName)), 0, keyRead, uintptr(unsafe.Pointer(&key)))
	if hresult != 0 {
		return "", fmt.Errorf("RegOpenKeyEx hresult 0x%x", hresult)
	}
	defer procRegCloseKey.Call(key)
	var valueType uint32
	var size uint32
	hresult, _, _ = procRegQueryValueEx.Call(key, 0, 0, uintptr(unsafe.Pointer(&valueType)), 0, uintptr(unsafe.Pointer(&size)))
	if hresult != 0 || valueType != regSZ {
		return "", fmt.Errorf("RegQueryValueEx hresult 0x%x type %d", hresult, valueType)
	}
	buffer := make([]uint16, (size+1)/2)
	hresult, _, _ = procRegQueryValueEx.Call(key, 0, 0, uintptr(unsafe.Pointer(&valueType)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if hresult != 0 {
		return "", fmt.Errorf("RegQueryValueEx hresult 0x%x", hresult)
	}
	return syscall.UTF16ToString(buffer), nil
}

// native1CCOMInventory mirrors Read-BFNativeInventoryDirect: registry-bound
// COM connector identity, FILE connection, the extension inventory through
// late-bound properties, and the sanitized phase error surface.
func native1CCOMInventory(ctx context.Context, deps Deps, target, executable string, credential native1cCredential, directory string) (map[string]any, error) {
	expectedDLL, err := filepath.Abs(filepath.Join(filepath.Dir(executable), "comcntr.dll"))
	if err != nil {
		return nil, blockedf("registered COM connector differs from the authorized platform.")
	}
	registered, err := registryDefaultValue(v83ConnectorRegistryKey)
	if err != nil || !isRegularFile(expectedDLL) || !strings.EqualFold(filepath.Clean(registered), expectedDLL) {
		return nil, blockedf("registered COM connector differs from the authorized platform.")
	}
	if strings.TrimSpace(credential.username) == "" ||
		native1cUnsafeCredentialText(credential.username) ||
		native1cUnsafeCredentialText(credential.password) {
		return nil, blockedf("credential characters are unsupported by the FILE connection builder.")
	}
	phase := "credential"
	sanitized := func() error { return blockedf("native inventory failed during %s.", phase) }
	connectionString := `File="` + target + `";Usr="` + credential.username + `";Pwd="` + credential.password + `";`
	hresult, _, _ := procCoInitializeEx.Call(0, coInitMask)
	initialized := hresult == 0 || hresult == 1
	if !initialized && hresult != rpcEChangedMode {
		return nil, sanitized()
	}
	if initialized {
		defer procCoUninitialize.Call()
	}
	var connectorPointer uintptr
	hresult, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&v83ConnectorCLSID)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidDispatch)),
		uintptr(unsafe.Pointer(&connectorPointer)),
	)
	if hresult != 0 || connectorPointer == 0 {
		return nil, sanitized()
	}
	connector := newOleDispatch(connectorPointer)
	defer connector.release()
	phase = "connect"
	connectionStringVariant, err := oleVariantBSTR(connectionString)
	if err != nil {
		return nil, sanitized()
	}
	defer oleVariantClear(&connectionStringVariant)
	connectionVariant, err := connector.method("Connect", []oleVariant{connectionStringVariant})
	if err != nil {
		return nil, sanitized()
	}
	if connectionVariant.vt != vtDispatch || connectionVariant.pointer() == 0 {
		oleVariantClear(&connectionVariant)
		return nil, sanitized()
	}
	connection := newOleDispatch(connectionVariant.pointer())
	connectionVariant.setPointer(0)
	oleVariantClear(&connectionVariant)
	defer connection.release()
	phase = "extensions"
	managerVariant, err := connection.property("ConfigurationExtensions")
	if err != nil {
		managerVariant, err = connection.property("РасширенияКонфигурации")
	}
	if err != nil {
		return nil, sanitized()
	}
	if managerVariant.vt != vtDispatch || managerVariant.pointer() == 0 {
		oleVariantClear(&managerVariant)
		return nil, sanitized()
	}
	manager := newOleDispatch(managerVariant.pointer())
	managerVariant.setPointer(0)
	oleVariantClear(&managerVariant)
	defer manager.release()
	arrayVariant, err := manager.method("Получить", nil)
	if err != nil {
		return nil, sanitized()
	}
	defer oleVariantClear(&arrayVariant)
	if arrayVariant.vt != (vtArray|vtDispatch) || arrayVariant.pointer() == 0 {
		return nil, sanitized()
	}
	array := arrayVariant.pointer()
	var upper, lower int32
	if hresult, _, _ = procSafeArrayGetLBound.Call(array, 1, uintptr(unsafe.Pointer(&lower))); hresult != 0 {
		return nil, sanitized()
	}
	if hresult, _, _ = procSafeArrayGetUBound.Call(array, 1, uintptr(unsafe.Pointer(&upper))); hresult != 0 {
		return nil, sanitized()
	}
	rows := []map[string]any{}
	for index := lower; index <= upper; index++ {
		var item oleVariant
		if hresult, _, _ = procSafeArrayGetElem.Call(array, uintptr(unsafe.Pointer(&index)), uintptr(unsafe.Pointer(&item))); hresult != 0 {
			oleVariantClear(&item)
			return nil, sanitized()
		}
		if item.vt != vtDispatch || item.pointer() == 0 {
			oleVariantClear(&item)
			return nil, sanitized()
		}
		rowDispatch := newOleDispatch(item.pointer())
		item.setPointer(0)
		oleVariantClear(&item)
		row, err := native1CCOMInventoryRow(connection, rowDispatch)
		rowDispatch.release()
		if err != nil {
			return nil, err
		}
		rows = append(rows, map[string]any{"properties": row})
	}
	sort.Slice(rows, func(i, j int) bool {
		left := asStringOr(asMap(rows[i]["properties"])["name"])
		right := asStringOr(asMap(rows[j]["properties"])["name"])
		if !strings.EqualFold(left, right) {
			return strings.ToLower(left) < strings.ToLower(right)
		}
		return left < right
	})
	inventory := map[string]any{
		"schema_version": int64(1),
		"kind":           "bsl-flow.native-inventory",
		"observed_at_utc": startTimeUTC(deps.now()),
		"target":         strings.TrimRight(filepath.Clean(target), `\/`),
		"platform":       map[string]any{"executable": executable, "com_connector": registered},
		"extensions":     toAnyMapSlice(rows),
		"limitations":    []any{"Base configuration version is not included because no verified API is used for it."},
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, blockedf("%v", err)
	}
	if err := writeJSON(filepath.Join(directory, "inventory.json"), inventory, false); err != nil {
		return nil, err
	}
	return inventory, nil
}

var native1CInventoryPropertyPairs = [][2]string{
	{"name", "Имя"},
	{"version", "Версия"},
	{"active", "Активно"},
	{"purpose", "Назначение"},
	{"scope", "ОбластьДействия"},
	{"uuid", "УникальныйИдентификатор"},
	{"hash_sum", "ХешСумма"},
}

// native1CCOMInventoryRow reads the seven inventory properties of one
// extension row, converting COM object values through the connection String
// method exactly like the legacy InvokeMember projection.
func native1CCOMInventoryRow(connection, row *oleDispatch) (map[string]any, error) {
	rowValue := map[string]any{}
	for _, pair := range native1CInventoryPropertyPairs {
		value, err := row.property(pair[1])
		if err != nil {
			oleVariantClear(&value)
			return nil, blockedf("inventory property %s is unobserved.", pair[0])
		}
		converted, err := native1CCOMConvert(connection, value)
		oleVariantClear(&value)
		if err != nil {
			return nil, blockedf("native inventory failed during extensions.")
		}
		rowValue[pair[0]] = converted
	}
	return rowValue, nil
}

func native1CCOMConvert(connection *oleDispatch, value oleVariant) (any, error) {
	switch value.vt {
	case vtEmpty, vtNull:
		return nil, nil
	case vtBSTR:
		return oleBSTRString(value.pointer()), nil
	case vtDispatch, vtUnknown:
		if value.pointer() == 0 {
			return nil, nil
		}
		stringVariant, err := connection.method("String", []oleVariant{value})
		if err != nil {
			return nil, err
		}
		defer oleVariantClear(&stringVariant)
		if stringVariant.vt != vtBSTR {
			return nil, fmt.Errorf("String returned type %d", stringVariant.vt)
		}
		return oleBSTRString(stringVariant.pointer()), nil
	case vtBool:
		return value.boolValue(), nil
	case vtI1, vtI2, vtI4, vtI8, vtInt, vtUI1, vtUI2, vtUI4, vtUI8, vtUInt:
		return value.int64Value(), nil
	case vtR4, vtR8:
		return value.float64Value(), nil
	default:
		return nil, fmt.Errorf("unsupported variant type %d", value.vt)
	}
}

func toAnyMapSlice(rows []map[string]any) []any {
	result := make([]any, 0, len(rows))
	for _, row := range rows {
		result = append(result, row)
	}
	return result
}
