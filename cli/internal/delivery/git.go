package delivery

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// gitDefaultTimeout is the legacy 120-second per-invocation bound.
	gitDefaultTimeout = 120 * time.Second
	// gitMaxOutputBytes is the legacy 64 MiB stdout/stderr capture ceiling.
	gitMaxOutputBytes = int64(67108864)

	gitEmptyConfigName     = "empty.gitconfig"
	gitEmptyAttributesName = "empty.gitattributes"
	gitEmptyHooksName      = "empty-hooks"
	gitProcessCwdName      = "cwd"
	gitIndexName           = "publication.index"
	gitStagedRecordsName   = "staged.json"
	gitCommitPointerName   = "commit.txt"
)

var (
	lsTreeIdentityPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	remoteHeadPattern     = regexp.MustCompile(`^([0-9a-f]{40}(?:[0-9a-f]{24})?)\t(.+)$`)
	authorEmailPattern    = regexp.MustCompile(`^[^<>\s@]+@[^<>\s@]+$`)
)

// GitPortConfig binds the real git-CLI adapter to one publication. ScratchDir
// is the controller-owned scratch directory: it must persist across process
// restarts of the same publication because the staging index, the staged
// records and the prepared commit pointer live there.
type GitPortConfig struct {
	// GitBinary is an explicit git executable; empty resolves git from PATH.
	GitBinary string
	// WorkDir is the worker worktree whose object database and files the
	// publication reads; the adapter never moves its HEAD or its own index.
	WorkDir string
	// ScratchDir is created when missing and must stay controller-owned.
	ScratchDir string
	// Baseline is the accepted parent commit; the worker HEAD must equal it.
	Baseline CommitID
	// AuthorName, AuthorEmail and CommitTime form the deterministic commit
	// identity; a zero CommitTime stamps the port construction instant.
	AuthorName  string
	AuthorEmail string
	CommitTime  time.Time
	// Timeout bounds every invocation; it defaults to 120s and must stay
	// within the legacy 1..120 seconds window.
	Timeout time.Duration
	// GitHubCLI is the gh executable used as the credential helper for the
	// github_cli HTTPS authorization profile.
	GitHubCLI string
}

// CLIGitPort is the production GitPort: it drives a real git CLI subprocess
// under the hardened publication profile of the legacy controller (argv-as-data,
// scrubbed environment, empty process working directory, bounded capture,
// per-call timeouts) and never falls back to any other transport.
type CLIGitPort struct {
	gitBinary   string
	gitHubCLI   string
	workDir     string
	baseline    CommitID
	authorName  string
	authorEmail string
	commitStamp string
	timeout     time.Duration

	maxOutputBytes  int64
	emptyConfig     string
	emptyAttributes string
	emptyHooks      string
	processCwd      string
	indexPath       string
	stagedPath      string
	commitPointer   string

	mutex             sync.Mutex
	indexReady        bool
	records           []stagedRecord
	recordsLoaded     bool
	commit            CommitID
	baselineTree      map[string]treeEntry
	baselineTreeReady bool
}

// stagedRecord persists exactly what one Stage call put into the staging
// index so a later process can verify the written tree without re-staging.
type stagedRecord struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	OID    string `json:"oid"`
	SHA256 string `json:"sha256"`
}

// treeEntry is one ls-tree record of the baseline tree.
type treeEntry struct {
	Mode string
	Type string
	OID  string
}

var _ GitPort = (*CLIGitPort)(nil)

// NewCLIGitPort validates the configuration, resolves the git executable and
// prepares the controller-owned scratch context (empty config, attributes,
// hooks and process working directory). A missing or unusable git binary is a
// typed blocker; there is no fallback.
func NewCLIGitPort(config GitPortConfig) (*CLIGitPort, error) {
	if !validCommitID(config.Baseline) {
		return nil, invalid("publication baseline is not an object identity")
	}
	if strings.TrimSpace(config.WorkDir) == "" || !filepath.IsAbs(config.WorkDir) || !filepath.IsAbs(config.ScratchDir) || strings.TrimSpace(config.ScratchDir) == "" {
		return nil, invalid("publication path must be an ordinary absolute filesystem path.")
	}
	if info, err := os.Stat(config.WorkDir); err != nil || !info.IsDir() {
		return nil, blocked("required publication directory is missing: %s", config.WorkDir)
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = gitDefaultTimeout
	}
	if timeout < time.Second || timeout > 120*time.Second {
		return nil, invalid("Git timeout must be between 1 and 120 seconds.")
	}
	gitBinary := config.GitBinary
	if gitBinary == "" {
		resolved, err := exec.LookPath("git")
		if err != nil {
			return nil, blocked("Git executable could not be resolved from PATH: %v", err)
		}
		gitBinary = resolved
	} else {
		if !filepath.IsAbs(gitBinary) {
			return nil, invalid("publication path must be an ordinary absolute filesystem path.")
		}
		if info, err := os.Stat(gitBinary); err != nil || info.IsDir() {
			return nil, blocked("required publication file is missing: %s", gitBinary)
		}
	}

	commitTime := config.CommitTime
	if commitTime.IsZero() {
		commitTime = time.Now().UTC()
	}
	port := &CLIGitPort{
		gitBinary:       gitBinary,
		gitHubCLI:       config.GitHubCLI,
		workDir:         config.WorkDir,
		baseline:        config.Baseline,
		authorName:      config.AuthorName,
		authorEmail:     config.AuthorEmail,
		commitStamp:     commitTime.UTC().Format(time.RFC3339),
		timeout:         timeout,
		maxOutputBytes:  gitMaxOutputBytes,
		emptyConfig:     filepath.Join(config.ScratchDir, gitEmptyConfigName),
		emptyAttributes: filepath.Join(config.ScratchDir, gitEmptyAttributesName),
		emptyHooks:      filepath.Join(config.ScratchDir, gitEmptyHooksName),
		processCwd:      filepath.Join(config.ScratchDir, gitProcessCwdName),
		indexPath:       filepath.Join(config.ScratchDir, gitIndexName),
		stagedPath:      filepath.Join(config.ScratchDir, gitStagedRecordsName),
		commitPointer:   filepath.Join(config.ScratchDir, gitCommitPointerName),
	}
	if err := port.prepareScratch(config.ScratchDir); err != nil {
		return nil, err
	}
	return port, nil
}

// prepareScratch creates and checks the context the legacy runner rebuilt per
// invocation: empty config and attributes files, an empty hooks directory and
// an empty process working directory.
func (p *CLIGitPort) prepareScratch(scratchDir string) error {
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		return blocked("controller-owned publication scratch directory is unavailable: %v", err)
	}
	for _, path := range []string{p.emptyConfig, p.emptyAttributes} {
		if info, err := os.Stat(path); err == nil {
			if info.Size() != 0 {
				return blocked("publication empty config or attributes file changed.")
			}
			continue
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			return blocked("controller-owned publication scratch directory is unavailable: %v", err)
		}
	}
	for _, directory := range []struct {
		path    string
		message string
	}{
		{p.emptyHooks, "publication hooks directory is not empty."},
		{p.processCwd, "publication Git working directory is not empty."},
	} {
		if err := os.MkdirAll(directory.path, 0o755); err != nil {
			return blocked("controller-owned publication scratch directory is unavailable: %v", err)
		}
		if err := assertDirectoryEmpty(directory.path); err != nil {
			return blocked("%s", directory.message)
		}
	}
	return nil
}

func assertDirectoryEmpty(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("directory is not empty")
	}
	return nil
}

// assertScratchInvariants re-checks the context before every invocation, the
// way the legacy runner validated its context per call.
func (p *CLIGitPort) assertScratchInvariants() error {
	for _, path := range []string{p.emptyConfig, p.emptyAttributes} {
		if info, err := os.Stat(path); err != nil || info.Size() != 0 {
			return blocked("publication empty config or attributes file changed.")
		}
	}
	if err := assertDirectoryEmpty(p.emptyHooks); err != nil {
		return blocked("publication hooks directory is not empty.")
	}
	if err := assertDirectoryEmpty(p.processCwd); err != nil {
		return blocked("publication Git working directory is not empty.")
	}
	return nil
}

func repoArguments(workDir string, args ...string) []string {
	return append([]string{"-C", workDir}, args...)
}

// profileForRemote derives the transport and authorization profile for a
// remote under the exact-target rules the domain already validated.
func (p *CLIGitPort) profileForRemote(remote string) (transport string, auth string, err error) {
	if strings.TrimSpace(remote) == "" || strings.ContainsAny(remote, "\x00\r\n") || strings.HasPrefix(remote, "-") {
		return "", "", invalid("publication remote is missing or malformed.")
	}
	if strings.Contains(remote, "://") {
		if githubRemotePattern.FindStringSubmatch(remote) == nil {
			return "", "", invalid("HTTPS remote must be exactly https://github.com/OWNER/REPO.git")
		}
		if strings.TrimSpace(p.gitHubCLI) == "" {
			return "", "", blocked("GitHub HTTPS publication requires the GitHub CLI credential helper.")
		}
		if info, statErr := os.Stat(p.gitHubCLI); statErr != nil || info.IsDir() {
			return "", "", blocked("required publication file is missing: %s", p.gitHubCLI)
		}
		return "https", "github_cli", nil
	}
	if !filepath.IsAbs(remote) {
		return "", "", invalid("publication remote must be an absolute local path or exactly https://github.com/OWNER/REPO.git")
	}
	return "file", "none", nil
}

// Stage records one manifest path into the controller-owned staging index.
// The blob is written with --no-filters from the worktree bytes and the index
// is populated through update-index --index-info, replicating the legacy
// byte-exact staging pipeline; baseline entries keep their regular mode and
// everything else is staged 100644.
func (p *CLIGitPort) Stage(path string) error {
	if err := validateStagingPath(path); err != nil {
		return err
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if err := p.loadBaselineTree(); err != nil {
		return err
	}
	if !p.indexReady {
		if err := p.runAssert(gitInvocation{
			operation:   "index-empty",
			label:       "empty index creation",
			arguments:   repoArguments(p.workDir, "read-tree", "--empty"),
			environment: map[string]string{"GIT_INDEX_FILE": p.indexPath},
		}); err != nil {
			return err
		}
		// A restart rebuilds staging from the accepted bytes; it never
		// trusts a saved index or its records.
		p.records = nil
		p.recordsLoaded = true
		p.indexReady = true
	}
	mode := "100644"
	if entry, ok := p.baselineTree[path]; ok {
		if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
			return blocked("symlink, submodule, or non-regular baseline entry is unsupported: %s", path)
		}
		mode = entry.Mode
	}
	source := filepath.Join(p.workDir, filepath.FromSlash(path))
	data, err := os.ReadFile(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return blocked("required publication file is missing: %s", source)
		}
		return blocked("delivery bytes could not be read for %s: %v", path, err)
	}
	blobResult, err := p.run(gitInvocation{
		operation: "hash-object",
		label:     "blob write for " + path,
		arguments: repoArguments(p.workDir, "hash-object", "--no-filters", "-w", "--stdin"),
		input:     data,
	})
	if err != nil {
		return err
	}
	if err := gitAssertSuccess(blobResult, "blob write for "+path); err != nil {
		return err
	}
	blobText, err := strictGitOutput(blobResult, "Git output")
	if err != nil {
		return err
	}
	blob := strings.TrimSpace(blobText)
	if !lsTreeIdentityPattern.MatchString(blob) {
		return blocked("malformed blob identity for %s", path)
	}
	indexRecord := []byte(mode + " " + blob + "\t" + path + "\x00")
	if err := p.runAssert(gitInvocation{
		operation:   "index-info",
		label:       "exact index population",
		arguments:   repoArguments(p.workDir, "update-index", "-z", "--index-info"),
		input:       indexRecord,
		environment: map[string]string{"GIT_INDEX_FILE": p.indexPath},
	}); err != nil {
		return err
	}
	p.records = append(p.records, stagedRecord{Path: path, Mode: mode, OID: blob, SHA256: sha256Hex(data)})
	if err := persistStagedRecords(p.stagedPath, p.records); err != nil {
		return blocked("publication staging records are unavailable: %v", err)
	}
	return nil
}

func validateStagingPath(path string) error {
	if path == "" || strings.ContainsRune(path, 0) || strings.HasPrefix(path, "/") || strings.Contains(path, `\`) {
		return invalid("manifest path must be a non-empty slash-separated relative path.")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.EqualFold(segment, ".git") {
			return invalid("unsafe manifest path: %s", path)
		}
	}
	return nil
}

// loadBaselineTree reads the accepted baseline tree once per process so
// staging can preserve baseline regular-file modes.
func (p *CLIGitPort) loadBaselineTree() error {
	if p.baselineTreeReady {
		return nil
	}
	result, err := p.run(gitInvocation{
		operation: "baseline-tree",
		label:     "baseline tree read",
		arguments: repoArguments(p.workDir, "ls-tree", "-rz", "--full-tree", string(p.baseline)),
	})
	if err != nil {
		return err
	}
	if err := gitAssertSuccess(result, "baseline tree read"); err != nil {
		return err
	}
	entries, err := parseLsTree(result.stdout)
	if err != nil {
		return err
	}
	p.baselineTree = entries
	p.baselineTreeReady = true
	return nil
}

// parseLsTree decodes `ls-tree -rz --full-tree` bytes under the legacy strict
// record grammar and error messages.
func parseLsTree(data []byte) (map[string]treeEntry, error) {
	entries := make(map[string]treeEntry)
	start := 0
	for index := 0; index < len(data); index++ {
		if data[index] != 0 {
			continue
		}
		record := data[start:index]
		start = index + 1
		if !utf8.Valid(record) {
			return nil, blocked("ls-tree record is not valid UTF-8.")
		}
		text := string(record)
		tab := strings.IndexByte(text, '\t')
		if tab < 0 {
			return nil, blocked("malformed ls-tree record.")
		}
		header := strings.Split(text[:tab], " ")
		if len(header) != 3 || !lsTreeIdentityPattern.MatchString(header[2]) {
			return nil, blocked("malformed ls-tree identity.")
		}
		path := text[tab+1:]
		if _, exists := entries[path]; exists {
			return nil, blocked("duplicate path in baseline tree.")
		}
		entries[path] = treeEntry{Mode: header[0], Type: header[1], OID: header[2]}
	}
	if start != len(data) {
		return nil, blocked("unterminated ls-tree output.")
	}
	return entries, nil
}

// loadStagedRecords returns the staged records of this process or the ones
// persisted by an earlier process of the same publication.
func (p *CLIGitPort) loadStagedRecords() ([]stagedRecord, error) {
	if p.recordsLoaded {
		return p.records, nil
	}
	data, err := os.ReadFile(p.stagedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			p.records = nil
			p.recordsLoaded = true
			return p.records, nil
		}
		return nil, blocked("publication staging records are unavailable.")
	}
	var records []stagedRecord
	if json.Unmarshal(data, &records) != nil {
		return nil, blocked("publication staging records are unavailable.")
	}
	for _, record := range records {
		if !validRelativePath(record.Path) ||
			(record.Mode != "100644" && record.Mode != "100755") ||
			!lsTreeIdentityPattern.MatchString(record.OID) ||
			!validSHA256(record.SHA256) {
			return nil, blocked("publication staging records are unavailable.")
		}
	}
	p.records = records
	p.recordsLoaded = true
	return p.records, nil
}

func persistStagedRecords(path string, records []stagedRecord) error {
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// assertWorkerHead verifies the worker HEAD still equals the accepted
// baseline under the legacy operation labels.
func (p *CLIGitPort) assertWorkerHead(operation string) error {
	result, err := p.run(gitInvocation{
		operation: operation,
		label:     "worker HEAD read",
		arguments: repoArguments(p.workDir, "rev-parse", "--verify", "HEAD^{commit}"),
	})
	if err != nil {
		return err
	}
	if err := gitAssertSuccess(result, "worker HEAD read"); err != nil {
		return err
	}
	text, err := strictGitOutput(result, "Git output")
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) != string(p.baseline) {
		return blocked("worker HEAD does not equal the accepted baseline.")
	}
	return nil
}

func (p *CLIGitPort) workerObjectFormat() (string, error) {
	result, err := p.run(gitInvocation{
		operation: "worker-object-format",
		label:     "Git object format read",
		arguments: repoArguments(p.workDir, "rev-parse", "--show-object-format"),
	})
	if err != nil {
		return "", err
	}
	if err := gitAssertSuccess(result, "Git object format read"); err != nil {
		return "", err
	}
	text, err := strictGitOutput(result, "Git output")
	if err != nil {
		return "", err
	}
	format := strings.TrimSpace(text)
	if format != "sha1" && format != "sha256" {
		return "", blocked("unsupported or inconsistent Git object format.")
	}
	if (format == "sha1" && len(p.baseline) != 40) || (format == "sha256" && len(p.baseline) != 64) {
		return "", blocked("unsupported or inconsistent Git object format.")
	}
	return format, nil
}

// CommitAll seals the staged manifest into the deterministic publication
// commit. It reproduces the legacy tree-building pipeline: write-tree from the
// exact staging index, verification of the final tree and every blob against
// the staged bytes, then commit-tree over the baseline with the pinned author
// and committer identity.
func (p *CLIGitPort) CommitAll(message string) (CommitID, error) {
	if strings.TrimSpace(p.authorName) == "" || strings.ContainsAny(p.authorName, "\x00\r\n") ||
		!authorEmailPattern.MatchString(p.authorEmail) ||
		strings.TrimSpace(message) == "" || strings.Contains(message, "\x00") {
		return "", invalid("invalid deterministic commit identity or message.")
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	records, err := p.loadStagedRecords()
	if err != nil {
		return "", err
	}
	if len(records) == 0 {
		return "", invalid("publication manifest is empty.")
	}
	if err := p.assertWorkerHead("worker-head-before"); err != nil {
		return "", err
	}
	if _, err := p.workerObjectFormat(); err != nil {
		return "", err
	}
	indexEnv := map[string]string{"GIT_INDEX_FILE": p.indexPath}
	treeResult, err := p.run(gitInvocation{
		operation:   "write-tree",
		label:       "tree creation",
		arguments:   repoArguments(p.workDir, "write-tree"),
		environment: indexEnv,
	})
	if err != nil {
		return "", err
	}
	if err := gitAssertSuccess(treeResult, "tree creation"); err != nil {
		return "", err
	}
	treeText, err := strictGitOutput(treeResult, "Git output")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(treeText)
	if !lsTreeIdentityPattern.MatchString(tree) {
		return "", blocked("created commit identity did not verify.")
	}
	verifyResult, err := p.run(gitInvocation{
		operation: "verify-tree",
		label:     "final tree read",
		arguments: repoArguments(p.workDir, "ls-tree", "-rz", "--full-tree", tree),
	})
	if err != nil {
		return "", err
	}
	if err := gitAssertSuccess(verifyResult, "final tree read"); err != nil {
		return "", err
	}
	verified, err := parseLsTree(verifyResult.stdout)
	if err != nil {
		return "", err
	}
	if len(verified) != len(records) {
		return "", blocked("final tree contains missing or extra files.")
	}
	for _, record := range records {
		entry, ok := verified[record.Path]
		if !ok || entry.Type != "blob" || entry.Mode != record.Mode || entry.OID != record.OID {
			return "", blocked("final tree path/mode/blob mismatch: %s", record.Path)
		}
		blobResult, err := p.run(gitInvocation{
			operation: "verify-blob",
			label:     "blob verification for " + record.Path,
			arguments: repoArguments(p.workDir, "cat-file", "blob", record.OID),
		})
		if err != nil {
			return "", err
		}
		if err := gitAssertSuccess(blobResult, "blob verification for "+record.Path); err != nil {
			return "", err
		}
		if sha256Hex(blobResult.stdout) != record.SHA256 {
			return "", blocked("final blob bytes mismatch: %s", record.Path)
		}
	}
	commitEnv := map[string]string{
		"GIT_AUTHOR_NAME":     p.authorName,
		"GIT_AUTHOR_EMAIL":    p.authorEmail,
		"GIT_AUTHOR_DATE":     p.commitStamp,
		"GIT_COMMITTER_NAME":  p.authorName,
		"GIT_COMMITTER_EMAIL": p.authorEmail,
		"GIT_COMMITTER_DATE":  p.commitStamp,
	}
	commitResult, err := p.run(gitInvocation{
		operation:   "commit-tree",
		label:       "commit creation",
		arguments:   repoArguments(p.workDir, "commit-tree", tree, "-p", string(p.baseline), "-F", "-"),
		input:       []byte(message),
		environment: commitEnv,
	})
	if err != nil {
		return "", err
	}
	if err := gitAssertSuccess(commitResult, "commit creation"); err != nil {
		return "", err
	}
	commitText, err := strictGitOutput(commitResult, "Git output")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(commitText)
	if !lsTreeIdentityPattern.MatchString(commit) {
		return "", blocked("created commit identity did not verify.")
	}
	checkResult, err := p.run(gitInvocation{
		operation: "commit-check",
		label:     "commit identity check",
		arguments: repoArguments(p.workDir, "rev-parse", "--verify", commit+"^{commit}"),
	})
	if err != nil {
		return "", err
	}
	if err := gitAssertSuccess(checkResult, "commit identity check"); err != nil {
		return "", err
	}
	checkText, err := strictGitOutput(checkResult, "Git output")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(checkText) != commit {
		return "", blocked("created commit identity did not verify.")
	}
	if err := p.saveCommitPointer(CommitID(commit)); err != nil {
		return "", err
	}
	p.commit = CommitID(commit)
	return p.commit, nil
}

// saveCommitPointer records the prepared commit for a later process of the
// same publication and fails closed if a different commit was already saved.
func (p *CLIGitPort) saveCommitPointer(commit CommitID) error {
	existing, err := os.ReadFile(p.commitPointer)
	if err == nil {
		if strings.TrimSpace(string(existing)) != string(commit) {
			return conflict("saved prepared commit differs from the exact accepted source.")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return blocked("push intent is missing its prepared commit.")
	}
	return writeFileAtomic(p.commitPointer, []byte(string(commit)+"\n"))
}

// publicationCommit returns the prepared commit of this process or the one
// persisted by an earlier process of the same publication.
func (p *CLIGitPort) publicationCommit() (CommitID, error) {
	if p.commit != "" {
		return p.commit, nil
	}
	data, err := os.ReadFile(p.commitPointer)
	if err != nil {
		return "", blocked("push intent is missing its prepared commit.")
	}
	commit := CommitID(strings.TrimSpace(string(data)))
	if !validCommitID(commit) {
		return "", invalid("publication commit identity is malformed.")
	}
	p.commit = commit
	return commit, nil
}

// Push dispatches the prepared commit to the exact remote ref exactly once
// per the lease contract: --force-with-lease=<ref>: rejects any existing ref,
// so only a definitively absent ref can be created.
func (p *CLIGitPort) Push(remote, ref string) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	commit, err := p.publicationCommit()
	if err != nil {
		return err
	}
	transport, auth, err := p.profileForRemote(remote)
	if err != nil {
		return err
	}
	if err := p.runAssert(gitInvocation{
		operation: "push-commit-check",
		label:     "publication commit check",
		arguments: repoArguments(p.workDir, "cat-file", "-e", string(commit)+"^{commit}"),
		transport: transport,
		auth:      auth,
	}); err != nil {
		return err
	}
	result, err := p.run(gitInvocation{
		operation: "push-create-only",
		label:     "push-create-only",
		arguments: repoArguments(p.workDir, "push", "--porcelain", "--no-verify",
			"--force-with-lease="+ref+":", remote, string(commit)+":"+ref),
		transport: transport,
		auth:      auth,
		keepAlive: true,
	})
	if err != nil {
		return err
	}
	if !result.completed {
		return blocked("push-create-only did not complete within the bounded wait.")
	}
	if result.exitCode != 0 {
		return blocked("push-create-only failed: %s", strings.TrimSpace(result.stderr))
	}
	return nil
}

// ReadRemoteHead answers the definitive head of remote/ref through
// check-ref-format and `ls-remote --exit-code --refs`: exit 0 with exactly
// one matching line is a known head, exit 2 with empty output is a known
// absent ref, and everything else is an undeterminable head reported as a
// typed cause with known=false.
func (p *CLIGitPort) ReadRemoteHead(remote, ref string) (CommitID, bool, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	transport, auth, err := p.profileForRemote(remote)
	if err != nil {
		return "", false, err
	}
	if err := p.runAssert(gitInvocation{
		operation: "check-ref-format",
		label:     "publication ref validation",
		arguments: []string{"check-ref-format", ref},
	}); err != nil {
		return "", false, err
	}
	result, err := p.run(gitInvocation{
		operation: "ls-remote",
		label:     "ls-remote",
		arguments: repoArguments(p.workDir, "ls-remote", "--exit-code", "--refs", remote, ref),
		transport: transport,
		auth:      auth,
	})
	if err != nil {
		return "", false, err
	}
	return classifyRemoteHead(result, ref)
}

// classifyRemoteHead maps an ls-remote result onto the GitPort contract with
// the legacy byte-exact blocker messages.
func classifyRemoteHead(result gitResult, ref string) (CommitID, bool, error) {
	if !result.completed {
		return "", false, blocked("ls-remote did not complete within the bounded wait.")
	}
	if result.exitCode == 2 && len(result.stdout) == 0 {
		return "", true, nil
	}
	if result.exitCode != 0 {
		return "", false, blocked("remote query did not return a verified present or absent ref.")
	}
	if !utf8.Valid(result.stdout) {
		return "", false, blocked("remote query did not return a verified present or absent ref.")
	}
	lines := make([]string, 0, 2)
	for _, line := range strings.Split(string(result.stdout), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		return "", false, blocked("remote returned an ambiguous or unexpected ref.")
	}
	match := remoteHeadPattern.FindStringSubmatch(lines[0])
	if match == nil || match[2] != ref {
		return "", false, blocked("remote returned an ambiguous or unexpected ref.")
	}
	return CommitID(match[1]), true, nil
}
