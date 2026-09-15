package repository

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

var registryActions = map[string]bool{
	"create":    true,
	"edit":      true,
	"activate":  true,
	"list":      true,
	"show":      true,
	"history":   true,
	"overview":  true,
	"archive":   true,
	"unarchive": true,
	"adopt":     true,
	"rebind":    true,
}

var executionActions = map[string]bool{
	"start":          true,
	"status":         true,
	"context":        true,
	"cancel":         true,
	"update":         true,
	"run":            true,
	"resume":         true,
	"next":           true,
	"record":         true,
	"accept":         true,
	"deliver":        true,
	"publish":        true,
	"publish-resume": true,
}

const defaultListLimit = 100

// Dispatch handles native repository commands. It retains the compatibility
// behavior of the original registry entrypoint; callers that have a lazy
// native host should use DispatchWithHost.
func Dispatch(args []string, stdout, stderr io.Writer) (bool, int) {
	return DispatchWithHost(args, stdout, stderr, nil)
}

// DispatchWithHost routes canonical UUIDs through the Go controller and leaves
// unrelated checkout-local legacy invocations to the existing PowerShell
// engine. A host is resolved lazily only when an activation or execution path
// actually needs the native provider.
func DispatchWithHost(args []string, stdout, stderr io.Writer, host *ControllerHost) (bool, int) {
	if len(args) < 2 || args[0] != "task" {
		return false, 0
	}
	if executionActions[args[1]] {
		// Keep the pre-native public compatibility behavior for status: the
		// legacy parser owns checkout-local status, while DispatchWithHost gives
		// the integrated CLI the native read path.
		if host == nil && (args[1] == "status" || args[1] == "context" || args[1] == "next" || args[1] == "cancel" || args[1] == "update" || args[1] == "start") {
			if args[1] == "next" || args[1] == "cancel" || args[1] == "update" || args[1] == "start" || args[1] == "context" {
				// These actions have no old registry behavior in the compatibility
				// wrapper; canonical calls are handled only when the host is wired.
				return dispatchCanonicalIfPresent(args, stdout, stderr, host)
			}
			return false, 0
		}
		return dispatchCanonicalIfPresent(args, stdout, stderr, host)
	}
	if !registryActions[args[1]] {
		return false, 0
	}
	code := executeWithHost(args, stdout, stderr, host)
	return true, code
}

func dispatchCanonicalIfPresent(args []string, stdout, stderr io.Writer, host *ControllerHost) (bool, int) {
	id := optionValue(args[2:], "--task")
	project := optionValue(args[2:], "--project")
	if !isUUID(id) || project == "" {
		return false, 0
	}
	repository, err := OpenRepository(project)
	if err != nil {
		// A canonical identity can only be delegated to the legacy controller
		// after the repository boundary has been verified.  If Git/common-dir
		// resolution fails, absence is unknown and falling through would allow a
		// legacy writer to claim the UUID.
		return true, writeError(stdout, stderr, err, wantsHuman(args))
	}
	present, err := canonicalTaskPresent(repository, id)
	if err != nil {
		// HasTask used to collapse all filesystem errors into false.  Preserve
		// the fail-closed distinction: only an actual ENOENT at the verified
		// canonical target permits checkout-local legacy routing.
		return true, writeError(stdout, stderr, err, wantsHuman(args))
	}
	if !present {
		return false, 0
	}
	human := wantsHuman(args)
	if _, err := repository.ReadTask(id); err != nil {
		return true, writeError(stdout, stderr, err, human)
	}
	payload, actionErr := executeControllerAction(args[1], project, id, args[2:], host)
	if actionErr != nil {
		return true, writeError(stdout, stderr, actionErr, human)
	}
	if human {
		fmt.Fprint(stdout, humanOutput(payload))
		return true, 0
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		fmt.Fprintln(stderr, err)
		return true, 11
	}
	return true, 0
}

// canonicalTaskPresent resolves only the target directory for an already
// verified repository.  A missing target is the sole result that authorizes
// legacy compatibility routing; malformed, inaccessible, or non-directory
// targets reserve the UUID and therefore return an error.
func canonicalTaskPresent(repository *Repository, id string) (bool, error) {
	directory, err := repository.taskDir(id)
	if err != nil {
		return false, err
	}
	if _, err := SafePath(directory); err != nil {
		return false, blocked("unsafe canonical task path: %v", err)
	}
	info, err := os.Lstat(directory)
	if err == nil {
		if !info.IsDir() {
			return false, blocked("canonical task target is not a directory: %s", id)
		}
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, blocked("cannot inspect canonical task target: %v", err)
}

func executeControllerAction(action, project, id string, args []string, host *ControllerHost) (any, error) {
	opts, err := parseControllerActionOptions(action, args)
	if err != nil {
		return nil, err
	}
	if opts.values["--project"] != project || opts.values["--task"] != id {
		return nil, invalid("controller identity does not match command options")
	}
	switch action {
	case "start":
		return nil, blocked("canonical task %s must be activated before execution", id)
	case "status":
		return commandControllerStatus(project, id)
	case "context":
		return commandControllerContext(project, id, host)
	case "next":
		return commandControllerNext(project, id)
	case "run":
		return controllerRun(project, id, host)
	case "resume":
		return commandControllerResume(project, id, host)
	case "record":
		return commandControllerRecord(project, id, opts.values["--attempt"], opts.values["--input"], host)
	case "accept":
		return commandControllerAccept(project, id, host)
	case "cancel":
		return commandControllerCancel(project, id)
	case "update":
		return commandControllerUpdate(project, id, opts.values["--input"], host)
	case "rebind":
		return commandControllerRebind(project, id, opts.values["--expected-revision"], opts.values["--input"], host)
	case "deliver":
		return nil, blocked("delivery is outside native controller activation")
	case "publish":
		return commandPublish(project, id, opts.values["--input"], false)
	case "publish-resume":
		return commandPublish(project, id, opts.values["--input"], true)
	default:
		return nil, invalid("unsupported controller action %s", action)
	}
}

// parseControllerActionOptions is deliberately separate from the registry
// parser.  The public CLI shares option names between legacy and native
// actions, but a canonical task gets a closed per-action contract before any
// controller method or provider is reached.
func parseControllerActionOptions(action string, args []string) (*options, error) {
	allowedValues := map[string]bool{"--project": true, "--task": true}
	allowedFlags := map[string]bool{"--human": true, "--json": true}

	// Explicit native selection is harmless and documents the route.  The
	// legacy value is handled as a blocker after parsing so it cannot trigger
	// fallback once canonical ownership has been established.
	switch action {
	case "status", "context", "next", "run", "resume", "cancel", "accept":
		allowedValues["--engine"] = true
		if action == "run" || action == "resume" {
			allowedValues["--runtime-auth"] = true
		}
	case "record":
		allowedValues["--engine"] = true
		allowedValues["--attempt"] = true
		allowedValues["--input"] = true
	case "update":
		allowedValues["--engine"] = true
		allowedValues["--input"] = true
		allowedValues["--runtime-auth"] = true
	case "rebind":
		allowedValues["--expected-revision"] = true
		allowedValues["--input"] = true
	case "publish", "publish-resume":
		allowedValues["--input"] = true
	}
	opts, err := parseOptions(args, allowedValues, allowedFlags)
	if err != nil {
		return nil, err
	}
	if err := opts.require("--project", "--task"); err != nil {
		return nil, err
	}
	if action == "publish" || action == "publish-resume" {
		if err := opts.require("--input"); err != nil {
			return nil, err
		}
	}
	if value := opts.values["--runtime-auth"]; value != "" && value != "stdin" {
		return nil, invalid("--runtime-auth accepts only stdin; credentials must not appear in arguments")
	}
	if engine := opts.values["--engine"]; engine != "" {
		switch engine {
		case "native":
			// Native is already selected by canonical identity.
		case "legacy-powershell":
			return nil, blocked("canonical repository tasks cannot select the legacy engine")
		default:
			return nil, invalid("--engine accepts only native or legacy-powershell")
		}
	}
	if action == "rebind" {
		if err := opts.require("--expected-revision", "--input"); err != nil {
			return nil, err
		}
	}
	if action == "update" {
		if err := opts.require("--input"); err != nil {
			return nil, err
		}
	}
	return opts, nil
}

func optionValue(args []string, key string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key {
			return args[index+1]
		}
	}
	return ""
}

func wantsHuman(args []string) bool {
	for _, arg := range args {
		if arg == "--human" {
			return true
		}
	}
	return false
}

func stripFlag(args []string, flag string) []string {
	result := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == flag {
			continue
		}
		result = append(result, arg)
	}
	return result
}

type options struct {
	values map[string]string
	flags  map[string]bool
}

func parseOptions(args []string, allowedValues, allowedFlags map[string]bool) (*options, error) {
	result := &options{values: map[string]string{}, flags: map[string]bool{}}
	for index := 0; index < len(args); index++ {
		token := args[index]
		switch {
		case strings.HasPrefix(token, "--"):
			if allowedFlags[token] {
				if result.flags[token] {
					return nil, invalid("repeated option %s", token)
				}
				result.flags[token] = true
				continue
			}
			if !allowedValues[token] {
				return nil, invalid("unknown or inapplicable option %s", token)
			}
			if result.values[token] != "" {
				return nil, invalid("repeated option %s", token)
			}
			if index+1 >= len(args) || args[index+1] == "" || strings.HasPrefix(args[index+1], "--") {
				return nil, invalid("missing value for %s", token)
			}
			result.values[token] = args[index+1]
			index++
		default:
			return nil, invalid("unexpected argument %s", token)
		}
	}
	return result, nil
}

func (o *options) require(keys ...string) error {
	for _, key := range keys {
		if o.values[key] == "" {
			return invalid("%s is required", key)
		}
	}
	return nil
}

func execute(args []string, stdout, stderr io.Writer) int {
	return executeWithHost(args, stdout, stderr, nil)
}

func executeWithHost(args []string, stdout, stderr io.Writer, host *ControllerHost) int {
	action := args[1]
	human := wantsHuman(args)
	if countFlag(args[2:], "--json") > 1 {
		return writeError(stdout, stderr, invalid("repeated option --json"), human)
	}
	rest := stripFlag(args[2:], "--json")
	var err error
	var payload any
	switch action {
	case "create":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--input": true}, map[string]bool{"--human": true})
		if err == nil {
			err = opts.require("--project", "--input")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandCreate(opts.values["--project"], opts.values["--input"])
		}
	case "edit":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--task": true, "--expected-revision": true, "--input": true}, map[string]bool{"--human": true})
		if err == nil {
			err = opts.require("--project", "--task", "--expected-revision", "--input")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandEdit(opts.values["--project"], opts.values["--task"], opts.values["--expected-revision"], opts.values["--input"])
		}
	case "activate":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--task": true, "--expected-revision": true, "--input": true}, map[string]bool{"--human": true})
		if err == nil {
			err = opts.require("--project", "--task")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandActivate(opts.values["--project"], opts.values["--task"], opts.values["--expected-revision"], opts.values["--input"], host)
		}
	case "archive", "unarchive":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--task": true, "--expected-revision": true}, map[string]bool{"--human": true})
		if err == nil {
			err = opts.require("--project", "--task", "--expected-revision")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandArchive(opts.values["--project"], opts.values["--task"], opts.values["--expected-revision"], action == "archive")
		}
	case "list":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--status": true, "--stage": true, "--priority": true, "--label": true, "--archived": true, "--updated-before": true, "--updated-after": true, "--limit": true, "--cursor": true}, map[string]bool{"--human": true})
		if err == nil {
			err = opts.require("--project")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandList(opts)
		}
	case "show":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--task": true}, map[string]bool{"--human": true})
		if err == nil {
			err = opts.require("--project", "--task")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandShow(opts.values["--project"], opts.values["--task"])
		}
	case "history":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--task": true}, map[string]bool{"--human": true})
		if err == nil {
			err = opts.require("--project", "--task")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandHistory(opts.values["--project"], opts.values["--task"])
		}
	case "overview":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--status": true, "--priority": true, "--label": true, "--archived": true}, map[string]bool{"--human": true})
		if err == nil {
			err = opts.require("--project")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandOverview(opts)
		}
	case "adopt":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--task": true, "--source": true, "--input": true}, map[string]bool{"--human": true, "--preview": true, "--apply": true})
		if err == nil {
			err = opts.require("--project", "--task")
		}
		if err == nil && opts.flags["--preview"] {
			err = opts.require("--source")
		}
		if err == nil && opts.flags["--apply"] {
			err = opts.require("--input")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandAdopt(opts.values["--project"], opts.values["--task"], opts.values["--source"], opts.values["--input"], opts.flags["--preview"], opts.flags["--apply"])
		}
	case "rebind":
		var opts *options
		opts, err = parseOptions(rest, map[string]bool{"--project": true, "--task": true, "--expected-revision": true, "--input": true}, map[string]bool{"--human": true})
		if err == nil {
			err = opts.require("--project", "--task", "--expected-revision", "--input")
		}
		if err == nil {
			human = opts.flags["--human"]
			payload, err = commandControllerRebind(opts.values["--project"], opts.values["--task"], opts.values["--expected-revision"], opts.values["--input"], host)
		}
	}
	if err != nil {
		return writeError(stdout, stderr, err, human)
	}
	if payload == nil {
		return writeError(stdout, stderr, blocked("command produced no result"), human)
	}
	if human {
		fmt.Fprint(stdout, humanOutput(payload))
		return 0
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		fmt.Fprintln(stderr, err)
		return 11
	}
	return 0
}

func countFlag(args []string, flag string) int {
	count := 0
	for _, arg := range args {
		if arg == flag {
			count++
		}
	}
	return count
}

func writeError(stdout, stderr io.Writer, err error, human bool) int {
	code := 11
	kind := "BF_BLOCKED"
	message := safeErrorMessage(err.Error())
	if typed, ok := err.(*KindError); ok {
		kind = typed.Kind
		if typed.Kind == "BF_INVALID" {
			code = 2
		}
	}
	if human {
		fmt.Fprintf(stderr, "%s: %s\n", kind, message)
		return code
	}
	envelope := map[string]any{
		"schema_version": 1,
		"task_id":        nil,
		"revision":       nil,
		"status":         "blocked",
		"stage":          nil,
		"next_action":    "inspect_blocker",
		"blockers":       []string{kind + ": " + message},
		"evidence_refs":  []string{},
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(envelope)
	return code
}

func openReady(project string) (*Repository, error) {
	repository, err := OpenRepository(project)
	if err != nil {
		return nil, err
	}
	if err := repository.EnsureIdentity(); err != nil {
		return nil, err
	}
	return repository, nil
}

func openReadOnly(project string) (*Repository, error) {
	repository, err := OpenRepository(project)
	if err != nil {
		return nil, err
	}
	if err := repository.LoadIdentity(); err != nil {
		return nil, err
	}
	return repository, nil
}

func readInput(path string) (map[string]any, error) {
	data, err := ReadFileBytes(path)
	if err != nil {
		return nil, invalid("cannot read input %s: %v", path, err)
	}
	object, err := DecodeObject(data)
	if err != nil {
		return nil, invalid("invalid input %s: %v", path, err)
	}
	return object, nil
}

func commandCreate(project, inputPath string) (any, error) {
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	input, err := readInput(inputPath)
	if err != nil {
		return nil, err
	}
	card, err := plannedCard(input, repository.Worktree)
	if err != nil {
		return nil, err
	}
	id, err := randomUUID()
	if err != nil {
		return nil, err
	}
	dependencies, _ := asStringSlice(card["depends_on"])
	unlock, err := repository.graphLock()
	if err != nil {
		return nil, conflict("dependency graph lock unavailable: %v", err)
	}
	if err := repository.validateGraph(id, dependencies); err != nil {
		unlock()
		return nil, err
	}
	state, err := repository.writeRevision(id, card, 0)
	unlock()
	if err != nil {
		return nil, err
	}
	return taskEnvelope(state, "planned"), nil
}

func commandEdit(project, id, expectedText, inputPath string) (any, error) {
	expected, err := strconv.ParseInt(expectedText, 10, 64)
	if err != nil || expected < 0 {
		return nil, invalid("--expected-revision must be a non-negative integer")
	}
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	patch, err := readInput(inputPath)
	if err != nil {
		return nil, err
	}
	card, err := patchCard(task.State, patch)
	if err != nil {
		return nil, err
	}
	dependencies, _ := asStringSlice(card["depends_on"])
	unlock, err := repository.graphLock()
	if err != nil {
		return nil, conflict("dependency graph lock unavailable: %v", err)
	}
	if err := repository.validateGraph(id, dependencies); err != nil {
		unlock()
		return nil, err
	}
	state, err := repository.writeRevision(id, card, expected)
	unlock()
	if err != nil {
		return nil, err
	}
	return taskEnvelope(state, ""), nil
}

func commandArchive(project, id, expectedText string, archived bool) (any, error) {
	expected, err := strconv.ParseInt(expectedText, 10, 64)
	if err != nil || expected < 0 {
		return nil, invalid("--expected-revision must be a non-negative integer")
	}
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	card := make(map[string]any, len(task.State)+1)
	for key, value := range task.State {
		card[key] = value
	}
	card["archived"] = archived
	card["updated_at"] = nowUTC()
	unlock, err := repository.graphLock()
	if err != nil {
		return nil, conflict("dependency graph lock unavailable: %v", err)
	}
	state, err := repository.writeRevision(id, card, expected)
	unlock()
	if err != nil {
		return nil, err
	}
	return taskEnvelope(state, ""), nil
}

func commandList(opts *options) (any, error) {
	repository, err := openReadOnly(opts.values["--project"])
	if err != nil {
		return nil, err
	}
	filters, err := filtersFromOptions(opts)
	if err != nil {
		return nil, err
	}
	if filters.Archived == nil {
		activeOnly := false
		filters.Archived = &activeOnly
	}
	if filters.Limit == 0 {
		filters.Limit = defaultListLimit
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		return nil, err
	}
	selected, next, err := applyFilters(rows, filters, repository.CloneID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"schema_version": 1,
		"repository_id":  repository.CloneID,
		"tasks":          selected,
		"diagnostics":    diagnostics,
		"next_cursor":    nullIfEmpty(next),
	}, nil
}

func commandOverview(opts *options) (any, error) {
	repository, err := openReadOnly(opts.values["--project"])
	if err != nil {
		return nil, err
	}
	rows, diagnostics, err := repository.Catalog()
	if err != nil {
		return nil, err
	}
	filters, err := filtersFromOptions(opts)
	if err != nil {
		return nil, err
	}
	selected, _, err := applyFilters(rows, filters, repository.CloneID)
	if err != nil {
		return nil, err
	}
	counters := Overview(selected, diagnostics)
	counters["schema_version"] = 1
	counters["repository_id"] = repository.CloneID
	counters["generated_at"] = nowUTC()
	return counters, nil
}

func commandShow(project, id string) (any, error) {
	repository, err := openReadOnly(project)
	if err != nil {
		return nil, err
	}
	row, err := repository.ResolveRow(id)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"schema_version": 1,
		"repository_id":  repository.CloneID,
		"task":           row,
	}
	if row.Source == "repository" {
		task, err := repository.ReadTask(id)
		if err != nil {
			return nil, err
		}
		payload["description"] = task.Description
		payload["provenance"] = task.Provenance
		payload["controller"] = controllerProjection(task.State, row)
	} else {
		resolved, _, err := repository.resolveLegacy(id)
		if err != nil {
			return nil, err
		}
		if resolved == nil || len(resolved.chain) == 0 {
			return nil, blocked("legacy task disappeared while reading: %s", id)
		}
		row.OriginWorktree = resolved.origin
		payload["task"] = row
		payload["controller"] = controllerProjection(resolved.chain[len(resolved.chain)-1], row)
	}
	return payload, nil
}

func commandHistory(project, id string) (any, error) {
	repository, err := openReadOnly(project)
	if err != nil {
		return nil, err
	}
	events, err := repository.History(id)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"schema_version": 1,
		"repository_id":  repository.CloneID,
		"task_id":        id,
		"events":         events,
	}
	if len(events) > 0 {
		if source, ok := events[len(events)-1]["source"].(string); ok && source != "" {
			payload["source"] = source
		}
		if projection, ok := events[len(events)-1]["controller"].(map[string]any); ok {
			payload["controller"] = projection
		}
	}
	return payload, nil
}

func filtersFromOptions(opts *options) (Filters, error) {
	filters := Filters{
		Status:        opts.values["--status"],
		Stage:         opts.values["--stage"],
		Priority:      opts.values["--priority"],
		Label:         opts.values["--label"],
		UpdatedBefore: opts.values["--updated-before"],
		UpdatedAfter:  opts.values["--updated-after"],
		Cursor:        opts.values["--cursor"],
	}
	if raw := opts.values["--archived"]; raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return filters, invalid("--archived must be true or false")
		}
		filters.Archived = &value
	}
	if raw := opts.values["--limit"]; raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			return filters, invalid("--limit must be between 1 and 1000")
		}
		filters.Limit = limit
	}
	return filters, nil
}

func taskEnvelope(state map[string]any, fallbackStatus string) map[string]any {
	status, _ := asString(state["lifecycle"])
	if status == "" {
		status = fallbackStatus
	}
	title, _ := asString(state["title"])
	priority, _ := asString(state["priority"])
	archived, _ := asBool(state["archived"])
	return map[string]any{
		"schema_version": 1,
		"task_id":        state["task_id"],
		"revision":       state["revision"],
		"status":         status,
		"archived":       archived,
		"priority":       priority,
		"title":          title,
		"next_action":    "inspect",
	}
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func sanitize(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character == '\t':
			builder.WriteString(`\t`)
		case character < 0x20 || character == 0x7f:
			builder.WriteString(fmt.Sprintf(`\x%02x`, character))
		default:
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func humanOutput(payload any) string {
	switch typed := payload.(type) {
	case map[string]any:
		if tasks, ok := typed["tasks"].([]Row); ok {
			var builder strings.Builder
			builder.WriteString(fmt.Sprintf("%-36s  %-10s  %-8s  %-20s  %s\n", "TASK", "STATUS", "PRIORITY", "UPDATED", "TITLE"))
			for _, task := range tasks {
				builder.WriteString(fmt.Sprintf("%-36s  %-10s  %-8s  %-20s  %s\n", sanitize(task.TaskID), sanitize(task.Status), sanitize(task.Priority), sanitize(task.UpdatedAt), sanitize(task.Title)))
			}
			if diagnostics, ok := typed["diagnostics"].([]map[string]any); ok && len(diagnostics) > 0 {
				builder.WriteString("\nDIAGNOSTICS\n")
				for _, entry := range diagnostics {
					builder.WriteString(fmt.Sprintf("%s  %s  %s  %s\n", sanitize(fmt.Sprint(entry["task_id"])), sanitize(fmt.Sprint(entry["health"])), sanitize(fmt.Sprint(entry["source"])), sanitize(fmt.Sprint(entry["detail"]))))
				}
			}
			return builder.String()
		}
		if task, ok := typed["task"].(Row); ok {
			var builder strings.Builder
			builder.WriteString(fmt.Sprintf("task_id: %s\nstatus: %s\npriority: %s\nrevision: %d\nupdated_at: %s\ntitle: %s\n", sanitize(task.TaskID), sanitize(task.Status), sanitize(task.Priority), task.Revision, sanitize(task.UpdatedAt), sanitize(task.Title)))
			if stage, ok := task.Stage.(string); ok && stage != "" {
				builder.WriteString(fmt.Sprintf("stage: %s\nnext_action: %s\n", sanitize(stage), sanitize(fmt.Sprint(task.NextAction))))
			}
			if task.Question != "" {
				builder.WriteString("question: " + sanitize(task.Question) + "\n")
			}
			for _, blocker := range task.Blockers {
				builder.WriteString("blocker: " + sanitize(blocker) + "\n")
			}
			if task.AttemptCount > 0 || task.AcceptanceCount > 0 {
				builder.WriteString(fmt.Sprintf("attempts: %d\nacceptances: %d\n", task.AttemptCount, task.AcceptanceCount))
			}
			if len(task.EvidenceRefs) > 0 {
				builder.WriteString("evidence_refs: " + sanitize(strings.Join(task.EvidenceRefs, ",")) + "\n")
			}
			if task.OriginWorktree != "" {
				builder.WriteString("origin_worktree: " + sanitize(task.OriginWorktree) + "\n")
			}
			if task.Health != "ok" {
				builder.WriteString("health: " + sanitize(task.Health) + "\n")
			}
			if description, ok := typed["description"].(string); ok && description != "" {
				builder.WriteString("description: " + sanitize(description) + "\n")
			}
			return builder.String()
		}
		if events, ok := typed["events"].([]map[string]any); ok {
			var builder strings.Builder
			for _, event := range events {
				builder.WriteString(fmt.Sprintf("revision %v: %v at %v sha256=%v\n", event["revision"], sanitize(fmt.Sprint(event["event"])), sanitize(fmt.Sprint(event["at"])), sanitize(fmt.Sprint(event["revision_sha256"]))))
			}
			return builder.String()
		}
		if _, ok := typed["total"]; ok {
			var builder strings.Builder
			for _, key := range []string{"total", "planned", "running", "needs_input", "blocked", "completed", "cancelled", "stale_completed", "archived", "dependency_blocked", "corrupt", "conflicts", "orphaned"} {
				builder.WriteString(fmt.Sprintf("%s: %v\n", key, typed[key]))
			}
			return builder.String()
		}
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(data) + "\n"
}
