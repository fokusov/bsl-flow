#Requires -Version 7.0
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $name)}
$script:checks=0
function Assert-E([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-E([scriptblock]$Action){try{& $Action|Out-Null;return ''}catch{return $_.Exception.Message}}
function Copy-E($Value){ConvertFrom-Json (ConvertTo-Json -InputObject $Value -Depth 100)}
function Write-E([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text)}
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-execution-profile-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
try {
    # Bytes identify fixtures; none of these executables is launched.
    $provider=Join-Path $testRoot 'provider.exe';Write-E $provider 'provider fixture'
    $sandbox=Join-Path $testRoot 'sandbox.exe';Write-E $sandbox 'sandbox fixture'
    $python=Join-Path $testRoot 'python.exe';Write-E $python 'python fixture'
    $profile=[pscustomobject]@{provider='opencode';executable=$provider;executable_sha256=Get-BFFileHash $provider;sandbox=[pscustomobject]@{executable=$sandbox;sha256=Get-BFFileHash $sandbox};toolset=[pscustomobject]@{name='cc-1c-skills';root=(Join-Path $testRoot 'toolset');sha256=('a'*64)};runtime=[pscustomobject]@{executable=$python;sha256=Get-BFFileHash $python;version='3.12.14';packages=@([pscustomobject]@{name='lxml';version='6.1.1'})};denied_read_roots=@((Join-Path $testRoot 'private'))}
    $budget=[pscustomobject]@{currency='USD';limit=10.0;reservation=0.05}
    $legacy=[pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Inspect this offline fixture.';mode='analysis_only';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@();provenance=[pscustomobject]@{source='user';reference='execution-profile-fixture';text='Inspect the fixture.'};models=[pscustomobject]@{worker='gpt-5.6-luna';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
    $request=Copy-E $legacy
    $request|Add-Member execution_profile $profile
    $request|Add-Member budget $budget
    $request.models=[pscustomobject]@{worker='deepseek/deepseek-v4-flash';worker_effort=$null;reviewer='deepseek/deepseek-v4-flash';reviewer_effort=$null}
    $schema=Join-Path $PackageRoot 'global/skills/1c-task/schemas/request.schema.json'
    foreach($valid in @($legacy,$request)){
        Assert-E ((Failure-E {Assert-BFRequest $valid}) -eq '') 'Valid legacy/OpenCode request rejected.'
        Assert-E (Test-Json -Json (ConvertTo-Json $valid -Depth 100) -SchemaFile $schema) 'Valid legacy/OpenCode schema rejected.'
    }
    $codex=Copy-E $request;$codex.execution_profile.provider='codex';$codex.models=Copy-E $legacy.models
    $codex.execution_profile|Add-Member codex_skills_sha256 ('f'*64)
    Assert-E ((Failure-E {Assert-BFRequest $codex}) -eq '') 'Profiled Codex Luna request rejected.'
    Assert-E (Test-Json -Json (ConvertTo-Json $codex -Depth 100) -SchemaFile $schema) 'Profiled Codex schema rejected.'
    $unica=Copy-E $request;$unica.execution_profile.toolset.name='unica'
    $unica.execution_profile.PSObject.Properties.Remove('runtime')
    $unica.execution_profile|Add-Member unica ([pscustomobject]@{plugin_root=(Join-Path $testRoot 'unica');bootstrap_sha256=('b'*64);manifest_sha256=('c'*64);runtime_cache=(Join-Path $testRoot 'runtime');allowed_tools=@('unica.code.search','unica.form.edit','unica.project.map','unica.code.patch')})
    Assert-E ((Failure-E {Assert-BFRequest $unica}) -eq '') 'Source-only Unica profile rejected.'
    Assert-E (Test-Json -Json (ConvertTo-Json $unica -Depth 100) -SchemaFile $schema) 'Source-only Unica schema rejected.'
    $invalid=@()
    $bad=Copy-E $request;$bad.models.worker='deepseek/another';$invalid+=,$bad
    $bad=Copy-E $request;$bad.models.reviewer='gpt-6-astra';$invalid+=,$bad
    $bad=Copy-E $request;$bad.models.worker_effort='medium';$invalid+=,$bad
    $bad=Copy-E $request;$bad.models.reviewer_effort='high';$invalid+=,$bad
    $bad=Copy-E $codex;$bad.models.worker_effort=$null;$invalid+=,$bad
    $bad=Copy-E $codex;$bad.models.reviewer_effort='ultra';$invalid+=,$bad
    $bad=Copy-E $codex;$bad.execution_profile.PSObject.Properties.Remove('codex_skills_sha256');$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile|Add-Member codex_skills_sha256 ('f'*64);$invalid+=,$bad
    $bad=Copy-E $legacy;$bad.models.worker='deepseek/deepseek-v4-flash';$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile=$null;$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile|Add-Member unexpected $true;$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile.sandbox.sha256='invalid';$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile.denied_read_roots=@();$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile.toolset.name='unica';$invalid+=,$bad
    $bad=Copy-E $unica;$bad.execution_profile.unica.allowed_tools=@('unica.runtime.job.start');$invalid+=,$bad
    $bad=Copy-E $unica;$bad.execution_profile.unica.allowed_tools=@('unica.support.edit');$invalid+=,$bad
    $bad=Copy-E $unica;$bad.execution_profile.unica.allowed_tools=@('unica.security.auth');$invalid+=,$bad
    $bad=Copy-E $unica;$bad.execution_profile.toolset.name='cc-1c-skills';$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile.PSObject.Properties.Remove('runtime');$invalid+=,$bad
    $bad=Copy-E $unica;$bad.execution_profile|Add-Member runtime (Copy-E $profile.runtime);$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile.runtime.version='3.12';$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile.runtime.executable=(Join-Path $testRoot 'python.txt');$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile.runtime.packages=@([pscustomobject]@{name='requests';version='1.0'});$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile.runtime.packages=@([pscustomobject]@{name='lxml';version='6.1.1'},[pscustomobject]@{name='lxml';version='6.1.1'});$invalid+=,$bad
    $bad=Copy-E $request;$bad.execution_profile.runtime|Add-Member unexpected $true;$invalid+=,$bad
    $bad=Copy-E $request;$bad.PSObject.Properties.Remove('budget');$invalid+=,$bad
    $bad=Copy-E $legacy;$bad|Add-Member budget (Copy-E $budget);$invalid+=,$bad
    $bad=Copy-E $request;$bad.budget.limit=-1.0;$invalid+=,$bad
    $bad=Copy-E $request;$bad.budget.limit=$null;$bad.budget.reservation=1.0;$invalid+=,$bad
    $bad=Copy-E $request;$bad.budget|Add-Member unexpected $true;$invalid+=,$bad
    foreach($bad in $invalid){
        Assert-E ((Failure-E {Assert-BFRequest $bad}) -match '^BF_INVALID:') 'Invalid request escaped controller validation.'
        Assert-E (-not (Test-Json -Json (ConvertTo-Json $bad -Depth 100) -SchemaFile $schema -ErrorAction SilentlyContinue)) 'Invalid request escaped schema validation.'
    }
    $legacyHash=Get-BFIntentHash $legacy;$changed=Copy-E $legacy;$changed.models.worker='other-model'
    Assert-E ((Get-BFIntentHash $changed) -ceq $legacyHash) 'Legacy intent hashing changed.'
    $profileHash=Get-BFIntentHash $request;$changed=Copy-E $request;$changed.execution_profile.toolset.sha256='b'*64
    Assert-E ((Get-BFIntentHash $changed) -cne $profileHash) 'Toolset identity omitted from intent.'
    $changed=Copy-E $codex;$changed.models.worker='gpt-6-astra'
    Assert-E ((Get-BFIntentHash $changed) -cne (Get-BFIntentHash $codex)) 'Managed model omitted from intent.'

    $state=[pscustomobject]@{task_id=$request.request_id;request=$request;project_path=(Join-Path $testRoot 'project');worker_path=(Join-Path $testRoot 'worker');intent_hash=$profileHash;policy_hash=('c'*64);baseline='baseline';classification=[pscustomobject]@{complexity='S';risk='low';impact_flags=@()};correction_rounds=0;repair=[pscustomobject]@{pending_failure='fixture'};status='ready';evidence=@()}
    # Real execution dependency hashing, with the unrelated toolset inventory isolated.
    & {
        function Test-BFToolsetSnapshot {param($Root,$ExpectedToolset);[pscustomobject]@{aggregate_sha256=('a'*64)}}
        $manifest=[pscustomobject]@{sha256=('d'*64)}
        foreach($stage in @('inspect','spec','spec_review','implement','code_review','verify','diagnose','acceptance')){
            $deps=Get-BFDependencies $state $stage $manifest
            Assert-E ($deps.Contains('execution')) "Execution identity omitted from $stage."
            $changed=Copy-E $state;$changed.request.models.reviewer='changed-model'
            Assert-E ((Get-BFDependencies $changed $stage $manifest).execution -cne $deps.execution) "Model mutation did not invalidate $stage."
        }
        Write-E $provider 'changed provider'
        Assert-E ((Failure-E {Get-BFDependencies $state 'inspect' $manifest}) -match 'provider executable changed') 'Provider byte drift accepted.'
        Write-E $provider 'provider fixture';Write-E $sandbox 'changed sandbox'
        Assert-E ((Failure-E {Get-BFDependencies $state 'inspect' $manifest}) -match 'sandbox executable changed') 'Sandbox byte drift accepted.'
        Write-E $sandbox 'sandbox fixture'
        Write-E $python 'changed python'
        Assert-E ((Failure-E {Get-BFDependencies $state 'inspect' $manifest}) -match 'pinned cc-1c-skills runtime executable changed') 'Runtime byte drift accepted.'
        Write-E $python 'python fixture'
        $changed=Copy-E $state;$changed.request.execution_profile.runtime.version='3.12.15'
        Assert-E ((Get-BFDependencies $changed 'inspect' $manifest).execution -cne $deps.execution) 'Pinned runtime identity omitted from execution binding.'
        $changed=Copy-E $state;$changed.request.execution_profile.toolset.sha256='e'*64
        Assert-E ((Failure-E {Get-BFDependencies $changed 'inspect' $manifest}) -match 'toolset differs') 'Toolset snapshot drift accepted.'
        $old=Copy-E $state;$old.request=$legacy
        Assert-E (-not (Get-BFDependencies $old 'inspect' $manifest).Contains('execution')) 'Legacy dependency shape changed.'
    }

    # Every provider receives the same stage, prompt, paths and cancellation callback.
    & {
        function Invoke-BFCodexWorker {param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled);@{provider='legacy';stage=$Stage;prompt=$Prompt;directory=$Directory;sandbox=$CodexPath;cancelled=& $Cancelled}}
        function Invoke-BFOpenCodeWorker {param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled);@{provider='opencode';stage=$Stage;prompt=$Prompt;directory=$Directory;sandbox=$CodexPath;cancelled=& $Cancelled}}
        function Invoke-BFProfiledCodexWorker {param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled);@{provider='codex';stage=$Stage;prompt=$Prompt;directory=$Directory;sandbox=$CodexPath;cancelled=& $Cancelled}}
        function Test-BFRuntimePreflight {param($State,$Directory);$null}
        function Assert-BFBudgetAdmission {param($State,$Directory);$null}
        function Add-BFBudgetReservation {param($State,$Directory,$Provider,$Stage,$RequestedModel);$null}
        function Complete-BFBudgetDispatch {param($State,$Directory,$Provider,$Stage,$RequestedModel);$null}
        foreach($route in @(@{request=$legacy;expected='legacy'},@{request=$request;expected='opencode'},@{request=$codex;expected='codex'})){
            foreach($stage in @('inspect','spec','implement','code_review','diagnose','spec_reconcile','code_reconcile')){
                $result=Invoke-BFManagedWorker ([pscustomobject]@{request=$route.request}) $stage 'prompt' $testRoot $sandbox {$true}
                Assert-E ($result.provider -eq $route.expected -and $result.stage -eq $stage -and $result.prompt -eq 'prompt' -and $result.directory -eq $testRoot -and $result.sandbox -eq $sandbox -and $result.cancelled) "Managed $stage arguments or provider were lost."
            }
        }
    }

    & {
        $script:runCalls=@();$script:runState=$state
        function Read-BFTask {param($ProjectPath,$TaskId);$script:runState}
        function Get-BFNext {param($State);@{action='dispatch';stage='inspect'}}
        function New-BFAttempt {param($ProjectPath,$TaskId,$CodexPath);$script:runCalls+=,$CodexPath;@{state=$script:runState}}
        function Invoke-BFStage {param($Run,$CodexPath,$StageExecutor);[pscustomobject]@{status='blocked'}}
        function Resolve-BFCodex {param($Path);throw 'Legacy resolver must not run for a profile.'}
        function Test-BFCodexCapability {throw 'Legacy capability must not run for a profile.'}
        function Test-BFRuntimePreflight {param($State,$Directory);$null}
        function Assert-BFBudgetAdmission {param($State,$Directory);$null}
        function Add-BFBudgetReservation {param($State,$Directory,$Provider,$Stage,$RequestedModel);$null}
        function Complete-BFBudgetDispatch {param($State,$Directory,$Provider,$Stage,$RequestedModel);$null}
        Invoke-BFRun $state.project_path $state.task_id ''|Out-Null
        Assert-E ($script:runCalls.Count -eq 1 -and $script:runCalls[0] -eq $sandbox) 'Profile sandbox was not selected before dispatch.'
        Invoke-BFRun $state.project_path $state.task_id $sandbox|Out-Null
        Assert-E ($script:runCalls.Count -eq 2) 'Matching explicit sandbox rejected.'
        Assert-E ((Failure-E {Invoke-BFRun $state.project_path $state.task_id $provider}) -match 'explicit CodexPath differs') 'Explicit different sandbox accepted.'
        Assert-E ($script:runCalls.Count -eq 2) 'Mismatched sandbox reached dispatch.'
    }

    & {
        # A profile critic still feeds the existing reconciliation stage and path.
        $script:reviewCalls=@()
        function Invoke-BFProfileSpecCritic {
            param($State,$Directory,$CodexPath,$Cancelled)
            $script:reviewCalls+='critic'
            Assert-E ($CodexPath -eq $sandbox -and -not (& $Cancelled)) 'Profile critic lost execution arguments.'
            Write-E (Join-Path (Get-BFChangePath $State) 'review.json') '{"fixture":"critic output"}'
        }
        function Invoke-BFProcess {throw 'Legacy critic process must not run for a profile.'}
        function Get-BFStagePrompt {param($State,$Stage,$Extra);$Extra}
        function Invoke-BFManagedWorker {
            param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled)
            $script:reviewCalls+='reconcile'
            Assert-E ($Stage -eq 'spec_reconcile' -and $Prompt.Contains('critic output') -and $CodexPath -eq $sandbox) 'Critic result did not reach managed reconciliation.'
            # Reconciliation did not finish: no final validation or success is fabricated.
            [pscustomobject]@{status='blocked'}
        }
        $reviewDirectory=Join-Path $testRoot 'spec-review';[void][IO.Directory]::CreateDirectory($reviewDirectory)
        $result=Invoke-BFSpecReviewStage $state $reviewDirectory $sandbox {$false}
        Assert-E ($result.status -eq 'blocked' -and ($script:reviewCalls -join ',') -eq 'critic,reconcile') 'Profile specification review skipped a stage.'
        Assert-E (Test-Path -LiteralPath (Join-Path $reviewDirectory 'review.json')) 'Normalized critic evidence was not retained.'
    }

    & {
        # Only process launch is mocked. Fresh original JUnit and evidence parsing stay real.
        $script:checkRoutes=@();$script:emitReport=$true
        $verify=Copy-E $state;$verify.request.mode='implement'
        $verify.request.criteria=@([pscustomobject]@{id='check';kind='unit';observation='Exact check passes.';executable=$provider;arguments=@('one','two');report='.bsl-flow-worker/check.xml';expected_tests=@('profile-check')})
        $script:reportPath=Join-Path $verify.worker_path '.bsl-flow-worker/check.xml'
        function Invoke-BFExecutionCheck {
            param($State,$Executable,$Arguments,$Directory,$CodexPath,$Cancelled)
            Assert-E ($Executable -eq $provider -and ($Arguments -join ',') -eq 'one,two' -and $CodexPath -eq $sandbox -and -not (& $Cancelled)) 'Managed check arguments changed.'
            $script:checkRoutes+='managed'
            if($script:emitReport){Write-E $script:reportPath '<testsuite tests="1"><testcase name="profile-check"/></testsuite>'}
            @{exit_code=0;stop_reason=$null}
        }
        function Get-BFPermissionProfile {param($Path,$Writable);'legacy-permissions'}
        function Invoke-BFProcess {
            param($Executable,$Arguments,$WorkingDirectory,$InputText,$Directory,$TimeoutSeconds,$Cancelled)
            Assert-E ($Executable -eq $sandbox -and $Arguments[2] -eq 'bsl_flow' -and $Arguments[-3] -eq $provider) 'Legacy sandbox command changed.'
            $script:checkRoutes+='legacy';Write-E $script:reportPath '<testsuite tests="1"><testcase name="profile-check"/></testsuite>';@{exit_code=0;stop_reason=$null}
        }
        Invoke-BFVerification $verify (Join-Path $testRoot 'managed-check') $sandbox {$false}|Out-Null
        Assert-E ($script:checkRoutes -join ',' -eq 'managed') 'Profile verification used the legacy process route.'
        $script:emitReport=$false
        Assert-E ((Failure-E {Invoke-BFVerification $verify (Join-Path $testRoot 'missing-report') $sandbox {$false}}) -match 'no original JUnit report') 'Profile verification reused stale JUnit.'
        $verify.request.PSObject.Properties.Remove('execution_profile')
        Invoke-BFVerification $verify (Join-Path $testRoot 'legacy-check') $sandbox {$false}|Out-Null
        Assert-E ($script:checkRoutes[-1] -eq 'legacy') 'Legacy verification route changed.'
        $verify.request|Add-Member execution_profile (Copy-E $profile)
        $verify.request.criteria=@([pscustomobject]@{id='native';kind='integration';native_1c=[pscustomobject]@{fixture=$true}})
        function Invoke-BFNativeVerification {param($State,$Criterion,$Directory,$Cancelled);$script:checkRoutes+='native';@{criterion_id=$Criterion.id;outcome='PASS';fixture=$true}}
        Invoke-BFVerification $verify (Join-Path $testRoot 'native-check') $sandbox {$false}|Out-Null
        Assert-E (($script:checkRoutes -join ',') -eq 'managed,managed,legacy,native') 'Profile changed native adapter routing.'
        $verify.request.criteria[0].PSObject.Properties.Remove('native_1c')
        Assert-E ((Failure-E {Invoke-BFVerification $verify (Join-Path $testRoot 'unbound-native') $sandbox {$false}}) -match 'temporary runtime restriction remains active') 'Profile bypassed unbound integration gate.'
    }
    Write-Output "TASK_EXECUTION_PROFILE_OK checks=$script:checks; model/sandbox/database processes=0"
} finally {
    $resolved=[IO.Path]::GetFullPath($testRoot)
    $temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')+[IO.Path]::DirectorySeparatorChar
    if(-not $resolved.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $resolved -Leaf) -notlike 'bsl-flow-execution-profile-*'){throw 'Unsafe test cleanup target.'}
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
