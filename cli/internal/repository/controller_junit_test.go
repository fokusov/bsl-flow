package repository

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestValidateNativeJUnitPassesExactNestedReport(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<testsuites tests="2" failures="0" errors="0" skipped="0" disabled="0">
  <testsuite tests="2" failures="0" errors="0" skipped="0" disabled="0" name="outer">
    <testsuite tests="2" failures="0" errors="0" skipped="0" disabled="0" name="inner">
      <testcase name="alpha"/>
      <testcase name="beta"/>
    </testsuite>
  </testsuite>
</testsuites>`)
	ok, err := validateNativeJUnit(data, []string{"alpha", "beta"})
	if err != nil || !ok {
		t.Fatalf("valid JUnit rejected: ok=%v err=%v", ok, err)
	}
}

func TestValidateNativeJUnitReportsKnownFailure(t *testing.T) {
	data := []byte(`<testsuite tests="2" failures="1" errors="1">
  <testcase name="alpha"><failure>assertion</failure></testcase>
  <testcase name="beta"><error>panic</error></testcase>
</testsuite>`)
	ok, err := validateNativeJUnit(data, []string{"alpha", "beta"})
	if err != nil || ok {
		t.Fatalf("known failure was not returned as false,nil: ok=%v err=%v", ok, err)
	}
}

func TestValidateNativeJUnitRejectsAggregateMismatchAndInvalidDigits(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`<testsuite tests="2"><testcase name="alpha"/></testsuite>`),
		[]byte(`<testsuite failures="01"><testcase name="alpha"/></testsuite>`),
		[]byte(`<testsuite errors="-1"><testcase name="alpha"/></testsuite>`),
		[]byte(`<testsuite skipped="1"><testcase name="alpha"/></testsuite>`),
	} {
		if ok, err := validateNativeJUnit(data, []string{"alpha"}); err == nil || ok {
			t.Fatalf("invalid aggregate accepted: ok=%v err=%v xml=%s", ok, err, data)
		}
	}
}

func TestValidateNativeJUnitRejectsUnsafeXMLAndNamespace(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`<!DOCTYPE testsuite [<!ENTITY x "unsafe">]><testsuite><testcase name="&x;"/></testsuite>`),
		[]byte(`<j:testsuite xmlns:j="urn:example"><j:testcase name="alpha"/></j:testsuite>`),
		[]byte(`<testsuite><testcase name="alpha"></testsuite>`),
		[]byte(`<testsuite><testcase name="alpha" a="1" a="2"/></testsuite>`),
	} {
		if ok, err := validateNativeJUnit(data, []string{"alpha"}); err == nil || ok {
			t.Fatalf("unsafe or malformed JUnit accepted: ok=%v err=%v xml=%s", ok, err, data)
		}
	}
}

func TestValidateNativeJUnitRejectsSkippedAndWrongSelection(t *testing.T) {
	skipped := []byte(`<testsuite tests="1" skipped="1"><testcase name="alpha"><skipped/></testcase></testsuite>`)
	if ok, err := validateNativeJUnit(skipped, []string{"alpha"}); err == nil || ok {
		t.Fatalf("skipped test accepted: ok=%v err=%v", ok, err)
	}

	data := []byte(`<testsuite tests="2"><testcase name="alpha"/><testcase name="beta"/></testsuite>`)
	for _, expected := range [][]string{{"alpha"}, {"alpha", "gamma"}, {"alpha", "alpha"}} {
		if ok, err := validateNativeJUnit(data, expected); err == nil || ok {
			t.Fatalf("wrong exact selection accepted: expected=%v ok=%v err=%v", expected, ok, err)
		}
	}
}

func TestValidateNativeJUnitSupportsUTF16BOM(t *testing.T) {
	text := `<testsuite tests="1"><testcase name="alpha"/></testsuite>`
	runes := utf16.Encode([]rune(text))
	data := make([]byte, 2+len(runes)*2)
	data[0], data[1] = 0xff, 0xfe
	for index, value := range runes {
		binary.LittleEndian.PutUint16(data[2+index*2:], value)
	}
	ok, err := validateNativeJUnit(data, []string{"alpha"})
	if err != nil || !ok {
		t.Fatalf("UTF-16 JUnit rejected: ok=%v err=%v", ok, err)
	}
}

func TestValidateNativeJUnitRejectsDeepOrHugeReports(t *testing.T) {
	deep := strings.Repeat("<x>", nativeJUnitMaxDepth) + `<testcase name="alpha"/>` + strings.Repeat("</x>", nativeJUnitMaxDepth)
	if ok, err := validateNativeJUnit([]byte(`<testsuite>`+deep+`</testsuite>`), []string{"alpha"}); err == nil || ok {
		t.Fatalf("deep JUnit accepted: ok=%v err=%v", ok, err)
	}
}
