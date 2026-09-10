#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PackageRoot 'global/skills/1c-task/scripts/Task.PublicationGit.ps1')

$script:checks = [Collections.Generic.List[string]]::new()
function Assert-PublicationTest {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "TEST_FAIL: $Message" }
    [void]$script:checks.Add($Message)
}
function Invoke-TestGit {
    param([string[]]$Arguments, [string]$Directory, [string]$Operation, [byte[]]$InputBytes = [byte[]]@(), [Collections.IDictionary]$Environment = @{})
    return Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess -Arguments $Arguments -Directory $Directory -Operation $Operation -InputBytes $InputBytes -Environment $Environment -Transport file) $Operation
}
function New-TestIndexBytes {
    param([object[]]$Entries)
    $stream = [IO.MemoryStream]::new()
    try {
        foreach ($entry in $Entries) {
            $bytes = [Text.UTF8Encoding]::new($false, $true).GetBytes(("{0} {1}`t{2}" -f $entry.mode,$entry.oid,$entry.path))
            $stream.Write($bytes,0,$bytes.Length);$stream.WriteByte(0)
        }
        return $stream.ToArray()
    } finally { $stream.Dispose() }
}

$root = Join-Path (Join-Path $PackageRoot 'work') ('publication-git-evidence-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($root)
$setup = Join-Path $root 'setup'; [void][IO.Directory]::CreateDirectory($setup)
$worker = Join-Path $root 'worker'; $remote = Join-Path $root 'remote.git'; $delivery = Join-Path $root 'delivery'; $publication = Join-Path $root 'publication'
[void](Invoke-TestGit @('init','--template=',$worker) $setup 'worker-init')
[void](Invoke-TestGit @('init','--bare','--template=',$remote) $setup 'remote-init')

$baselineFiles = [ordered]@{
    'bin/crlf.bin' = [byte[]](0,13,10,255,65,10)
    'scripts/run.sh' = [Text.UTF8Encoding]::new($false).GetBytes("#!/bin/sh`necho baseline`n")
    'deleted.txt' = [Text.UTF8Encoding]::new($false).GetBytes("remove me`n")
}
$baselineEntries = @()
foreach ($path in $baselineFiles.Keys) {
    $blob = (Invoke-TestGit @('-C',$worker,'hash-object','--no-filters','-w','--stdin') $setup 'baseline-blob' $baselineFiles[$path]).stdout.Trim()
    $baselineEntries += [ordered]@{path=$path;mode=if ($path -eq 'scripts/run.sh') {'100755'} else {'100644'};oid=$blob}
}
$baselineIndex = Join-Path $root 'baseline.index'; $baselineEnvironment = @{BF_INTERNAL_GIT_INDEX_FILE=$baselineIndex}
[void](Invoke-TestGit @('-C',$worker,'read-tree','--empty') $setup 'baseline-index-empty' -Environment $baselineEnvironment)
[void](Invoke-TestGit @('-C',$worker,'update-index','-z','--index-info') $setup 'baseline-index' (New-TestIndexBytes $baselineEntries) $baselineEnvironment)
$baselineTree = (Invoke-TestGit @('-C',$worker,'write-tree') $setup 'baseline-tree' -Environment $baselineEnvironment).stdout.Trim()
$baseMetadata = @{BF_INTERNAL_GIT_AUTHOR_NAME='Publication Test';BF_INTERNAL_GIT_AUTHOR_EMAIL='publication@example.invalid';BF_INTERNAL_GIT_AUTHOR_DATE='2026-09-10T10:00:00Z';BF_INTERNAL_GIT_COMMITTER_NAME='Publication Test';BF_INTERNAL_GIT_COMMITTER_EMAIL='publication@example.invalid';BF_INTERNAL_GIT_COMMITTER_DATE='2026-09-10T10:00:00Z'}
$baseline = (Invoke-TestGit @('-C',$worker,'commit-tree',$baselineTree,'-F','-') $setup 'baseline-commit' ([Text.UTF8Encoding]::new($false).GetBytes('Baseline')) $baseMetadata).stdout.Trim()
[void](Invoke-TestGit @('-C',$worker,'update-ref','refs/heads/main',$baseline) $setup 'baseline-ref')
[void](Invoke-TestGit @('-C',$worker,'symbolic-ref','HEAD','refs/heads/main') $setup 'baseline-head')

$hookMarker = Join-Path $root 'hook-ran.txt'; $filterMarker = Join-Path $root 'filter-ran.txt'; $envMarker = Join-Path $root 'env-helper-ran.txt'
$hookPath = Join-Path $worker '.git\hooks\pre-push'
[void][IO.Directory]::CreateDirectory((Split-Path $hookPath -Parent))
[IO.File]::WriteAllText($hookPath, "#!/bin/sh`nprintf hook > '$($hookMarker.Replace('\','/'))'`nexit 1`n", [Text.UTF8Encoding]::new($false))
[void](Invoke-TestGit @('-C',$worker,'config','core.hooksPath',(Split-Path $hookPath -Parent)) $setup 'hostile-hook-config')
[void](Invoke-TestGit @('-C',$worker,'config','filter.evil.clean',("powershell.exe -NoProfile -Command `"Set-Content -LiteralPath '$filterMarker' evil; `$input`"")) $setup 'hostile-filter-config')

$source = Join-Path $delivery 'source'; [void][IO.Directory]::CreateDirectory((Join-Path $source 'bin')); [void][IO.Directory]::CreateDirectory((Join-Path $source 'scripts')); [void][IO.Directory]::CreateDirectory((Join-Path $source 'данные'))
$binary = [byte[]](255,0,13,10,66,13,10,0)
$scriptBytes = [Text.UTF8Encoding]::new($false).GetBytes("#!/bin/sh`necho changed`n")
$unicodeBytes = [Text.UTF8Encoding]::new($false).GetBytes("строка 1`r`nстрока 2`n")
$attributesBytes = [Text.UTF8Encoding]::new($false).GetBytes("*.bin filter=evil`n")
[IO.File]::WriteAllBytes((Join-Path $source 'bin\crlf.bin'),$binary)
[IO.File]::WriteAllBytes((Join-Path $source 'scripts\run.sh'),$scriptBytes)
[IO.File]::WriteAllBytes((Join-Path $source 'данные\файл с пробелом.txt'),$unicodeBytes)
[IO.File]::WriteAllBytes((Join-Path $source '.gitattributes'),$attributesBytes)
$manifestFiles = @(
    [ordered]@{path='.gitattributes';sha256=Get-BFPublicationBytesSha256 $attributesBytes;deleted=$false},
    [ordered]@{path='bin/crlf.bin';sha256=Get-BFPublicationBytesSha256 $binary;deleted=$false},
    [ordered]@{path='deleted.txt';sha256=$null;deleted=$true},
    [ordered]@{path='scripts/run.sh';sha256=Get-BFPublicationBytesSha256 $scriptBytes;deleted=$false},
    [ordered]@{path='данные/файл с пробелом.txt';sha256=Get-BFPublicationBytesSha256 $unicodeBytes;deleted=$false}
)
$manifest = [ordered]@{schema_version=1;baseline=$baseline;files=$manifestFiles;sha256='test-manifest'}
$state = [ordered]@{worker_path=$worker;baseline=$baseline}
$request = [ordered]@{remote=$remote;ref='refs/heads/codex/publication-git-test';auth='none'}
$metadata = [ordered]@{name='Publication Test';email='publication@example.invalid';message="Accepted snapshot`nwith Unicode: тест";timestamp='2026-09-10T12:34:56Z'}

$savedEnvironment = [ordered]@{}
foreach ($key in @('GIT_INDEX_FILE','GIT_OBJECT_DIRECTORY','GIT_CONFIG_COUNT','GIT_CONFIG_KEY_0','GIT_CONFIG_VALUE_0','HTTP_PROXY','HTTPS_PROXY')) { $savedEnvironment[$key] = [Environment]::GetEnvironmentVariable($key,'Process') }
try {
    $env:GIT_INDEX_FILE = Join-Path $root 'hostile.index'
    $env:GIT_OBJECT_DIRECTORY = Join-Path $root 'hostile-objects'
    $env:GIT_CONFIG_COUNT = '1'; $env:GIT_CONFIG_KEY_0 = 'credential.helper'; $env:GIT_CONFIG_VALUE_0 = "!powershell.exe -NoProfile -Command `"Set-Content -LiteralPath '$envMarker' injected`""
    $env:HTTP_PROXY = 'http://127.0.0.1:1'; $env:HTTPS_PROXY = 'http://127.0.0.1:1'
    $dependencies = Get-BFPublicationGitDependencies -Publication $request
    Assert-PublicationTest ($dependencies.git.path -ceq 'C:\Program Files\Git\mingw64\bin\git.exe' -and $dependencies.git_frontend.path -ceq 'C:\Program Files\Git\cmd\git.exe' -and $dependencies.git.sha256 -match '^[0-9a-f]{64}$') 'fixed Git installation and actual executable identities are exposed'
    Assert-PublicationTest ($dependencies.git_shell.sha256 -match '^[0-9a-f]{64}$' -and $dependencies.git_remote_https.sha256 -match '^[0-9a-f]{64}$') 'Git transport executable identities are exposed'
    $httpsDependencies = Get-BFPublicationGitDependencies -Publication ([ordered]@{remote='https://github.com/Owner/Repo.git';ref='refs/heads/codex/publication-git-test';auth='github_cli'})
    Assert-PublicationTest ($httpsDependencies.remote -ceq 'https://github.com/owner/repo.git' -and $httpsDependencies.gh.sha256 -match '^[0-9a-f]{64}$') 'GitHub profile canonicalizes the ledger key and binds the fixed gh executable'
    $prepared = New-BFPublicationCommit -State $state -DeliveryPath $delivery -Manifest $manifest -Directory $publication -Metadata $metadata -Publication $request
} finally {
    foreach ($key in $savedEnvironment.Keys) {
        if($null -eq $savedEnvironment[$key]){Remove-Item -LiteralPath ('Env:'+ $key) -ErrorAction SilentlyContinue}
        else{[Environment]::SetEnvironmentVariable($key,$savedEnvironment[$key],'Process')}
    }
}
$environmentRestored=$true
foreach($key in $savedEnvironment.Keys){
    $actual=[Environment]::GetEnvironmentVariable($key,'Process')
    if(($null -eq $savedEnvironment[$key] -and $null -ne $actual) -or ($null -ne $savedEnvironment[$key] -and $actual -cne $savedEnvironment[$key])){$environmentRestored=$false}
}
Assert-PublicationTest $environmentRestored 'hostile-environment test restores absent and empty variables exactly'

Assert-PublicationTest ($prepared.parent_oid -ceq $baseline) 'prepared commit has the exact accepted baseline parent'
$longPublication = Join-Path $root ('long-path\' + ('a' * 70) + '\' + ('b' * 70) + '\' + ('c' * 70))
$repeat = New-BFPublicationCommit -State $state -DeliveryPath $delivery -Manifest $manifest -Directory $longPublication -Metadata $metadata -Publication $request
Assert-PublicationTest ($repeat.commit_oid -ceq $prepared.commit_oid -and $repeat.tree_oid -ceq $prepared.tree_oid -and $repeat.staging_directory.Length -gt 260 -and $repeat.repository.Length -lt 260) 'fixed metadata is deterministic and owned object storage supports production-length Windows staging paths'
$parentLine = (Invoke-TestGit @('--git-dir',$prepared.repository,'rev-list','--parents','-n','1',$prepared.commit_oid) $publication 'verify-parent').stdout.Trim()
Assert-PublicationTest ($parentLine -ceq ($prepared.commit_oid + ' ' + $baseline)) 'commit graph preserves the exact parent'
$treeResult = Invoke-TestGit @('--git-dir',$prepared.repository,'ls-tree','-rz','--full-tree',$prepared.tree_oid) $publication 'test-final-tree'
$tree = ConvertFrom-BFPublicationLsTree $treeResult.stdout_bytes
Assert-PublicationTest ($tree.Count -eq 4 -and -not $tree.Contains('deleted.txt')) 'final tree contains only non-deleted manifest files'
Assert-PublicationTest ($tree['scripts/run.sh'].mode -ceq '100755' -and $tree['данные/файл с пробелом.txt'].mode -ceq '100644') 'baseline executable mode is preserved and new files use 100644'
$raw = Invoke-TestGit @('--git-dir',$prepared.repository,'cat-file','blob',$tree['bin/crlf.bin'].oid) $publication 'test-binary'
Assert-PublicationTest (-not $raw.stdout_text_valid -and [Convert]::ToHexString($raw.stdout_bytes) -ceq [Convert]::ToHexString($binary)) 'raw binary and CRLF bytes survive without text decoding'
Assert-PublicationTest (-not (Test-Path -LiteralPath $hookMarker) -and -not (Test-Path -LiteralPath $filterMarker) -and -not (Test-Path -LiteralPath $envMarker)) 'hooks, filters, and inherited Git configuration did not execute'

$before = Get-BFPublicationRemote -Repository $prepared.repository -Publication $request -Directory $publication
Assert-PublicationTest ($before.completed -and $before.exit_code -eq 2 -and $null -eq $before.oid) 'exact remote ref is initially absent'
$firstPush = Send-BFPublicationCommit -Repository $prepared.repository -CommitOid $prepared.commit_oid -Publication $request -Directory $publication
Assert-PublicationTest ($firstPush.completed -and $firstPush.exit_code -eq 0 -and $firstPush.process.non_interruptible) 'create-only push succeeds and records non-interruptible process identity'
Assert-PublicationTest (-not (Test-Path -LiteralPath $hookMarker) -and -not (Test-Path -LiteralPath $envMarker)) 'push did not execute worker hooks or inherited credential helpers'
$after = Get-BFPublicationRemote -Repository $prepared.repository -Publication $request -Directory $publication
Assert-PublicationTest ($after.completed -and $after.exit_code -eq 0 -and $after.oid -ceq $prepared.commit_oid) 'exact remote read returns the intended commit'

$nextEnvironment = @{BF_INTERNAL_GIT_AUTHOR_NAME='Publication Test';BF_INTERNAL_GIT_AUTHOR_EMAIL='publication@example.invalid';BF_INTERNAL_GIT_AUTHOR_DATE='2026-09-10T12:35:56Z';BF_INTERNAL_GIT_COMMITTER_NAME='Publication Test';BF_INTERNAL_GIT_COMMITTER_EMAIL='publication@example.invalid';BF_INTERNAL_GIT_COMMITTER_DATE='2026-09-10T12:35:56Z'}
$nextCommit = (Invoke-TestGit @('--git-dir',$prepared.repository,'commit-tree',$prepared.tree_oid,'-p',$prepared.commit_oid,'-F','-') $publication 'next-commit' ([Text.UTF8Encoding]::new($false).GetBytes('Different commit')) $nextEnvironment).stdout.Trim()
$blocked = Send-BFPublicationCommit -Repository $prepared.repository -CommitOid $nextCommit -Publication $request -Directory (Join-Path $publication 'second-push')
Assert-PublicationTest ($blocked.completed -and $blocked.exit_code -ne 0) 'empty expected lease rejects an existing ref'
$unchanged = Get-BFPublicationRemote -Repository $prepared.repository -Publication $request -Directory $publication
Assert-PublicationTest ($unchanged.oid -ceq $prepared.commit_oid) 'rejected create-only push leaves the remote ref unchanged'
Assert-PublicationTest (@(Get-ChildItem -LiteralPath (Join-Path $publication 'processes') -Directory | ForEach-Object { Test-Path -LiteralPath (Join-Path $_.FullName 'process.json') } | Where-Object { -not $_ }).Count -eq 0) 'every launched Git process has a durable identity receipt'

[ordered]@{status='PASS';checks=$script:checks;evidence_root=$root;commit_oid=$prepared.commit_oid;tree_oid=$prepared.tree_oid;parent_oid=$prepared.parent_oid;remote_ref=$request.ref;external_network_calls=0} | ConvertTo-Json -Depth 5
