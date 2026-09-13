#Requires -Version 7.0
# ADR index: read-only, derived projection over the normative ADR text.
# The index references decisions and architectural subjects; it is not a
# second policy engine and creates no authorization or transition authority.
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Task.Storage.ps1')
. (Join-Path $PSScriptRoot 'Task.Memory.ps1')

# Stage bundle bounds. They are deliberately small: an architecture bundle is a
# short instructional context, not a documentation dump or a policy engine.
$script:BFArchitectureBundleMaxDecisions = 8
$script:BFArchitectureBundleMaxExcerpt = 500
$script:BFArchitectureBundleMaxChars = 6000
$script:BFArchitectureBundleMaxExcluded = 16
$script:BFADRSectionCache = @{}

function Get-BFArchitectureRoot {
    # scripts -> 1c-task -> skills -> global -> package root
    return Split-Path (Split-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) -Parent) -Parent
}

function Get-BFArchitectureIndexPath {
    param([string]$RepositoryRoot)
    $root = if([string]::IsNullOrWhiteSpace($RepositoryRoot)){Get-BFArchitectureRoot}else{Assert-BFSafePath $RepositoryRoot}
    return Join-Path $root 'docs/architecture/adr-index.json'
}

function Get-BFArchitectureTaskDirectory {
    # Self-contained task-directory resolver so the architecture module does not
    # depend on Task.Engine or Task.Contracts.
    param([string]$ProjectPath,[string]$TaskId)
    if($TaskId -cnotmatch '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'){throw (New-BFError 'BF_INVALID' 'Task identity must be a canonical lower-case UUID.')}
    return Assert-BFSafePath (Join-Path $ProjectPath ('.bsl-flow/tasks/' + $TaskId))
}

function Get-BFArchitectureValue {
    # Self-contained property accessor so the architecture module does not
    # depend on Task.Contracts.
    param($Value,[string]$Name,$Default=$null)
    if($Value -is [System.Collections.IDictionary]){if($Value.Contains($Name)){return $Value[$Name]}}
    elseif($null -ne $Value -and $null -ne $Value.PSObject.Properties[$Name]){return $Value.$Name}
    return $Default
}

function Assert-BFArchitectureRelativePath {
    # Self-contained variant of the controller relative-path guard so the
    # architecture module does not depend on Task.Contracts.
    param([string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value) -or [IO.Path]::IsPathRooted($Value) -or $Value -match '[:*?"<>|\x00-\x1f]' -or $Value -match '(^|[\\/])\.\.([\\/]|$)') { throw (New-BFError 'BF_INVALID' ("Unsafe relative architecture path: {0}." -f $Value)) }
}

function Get-BFStringSha256 {
    param([AllowNull()][string]$Text)
    $bytes=[Text.Encoding]::UTF8.GetBytes([string]$Text)
    return ([BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash($bytes))).Replace('-','').ToLowerInvariant()
}

function Resolve-BFArchitectureContext {
    # Single resolver for context, stage prompt and dependencies. A project index
    # wins; otherwise the package root is used; otherwise the scope is missing.
    param([string]$ProjectPath,[string]$FallbackRoot)
    $package = if([string]::IsNullOrWhiteSpace($FallbackRoot)){Get-BFArchitectureRoot}else{Assert-BFSafePath $FallbackRoot}
    if(-not [string]::IsNullOrWhiteSpace($ProjectPath)){
        $project=Assert-BFSafePath $ProjectPath
        if(Test-Path -LiteralPath (Join-Path $project 'docs/architecture/adr-index.json') -PathType Leaf){return [ordered]@{root=$project;scope='project'}}
    }
    if(Test-Path -LiteralPath (Join-Path $package 'docs/architecture/adr-index.json') -PathType Leaf){return [ordered]@{root=$package;scope='package'}}
    $fallback = if(-not [string]::IsNullOrWhiteSpace($ProjectPath)){Assert-BFSafePath $ProjectPath}else{$package}
    return [ordered]@{root=$fallback;scope='missing'}
}

function Get-BFArchitectureContextRoot {
    # Stage E: a project may opt in with its own docs/architecture/adr-index.json.
    # Without it the previous package default root (and its missing_context
    # fallback) is unchanged.
    param([string]$ProjectPath,[string]$FallbackRoot)
    return [string](Resolve-BFArchitectureContext $ProjectPath $FallbackRoot).root
}

function Test-BFPathContained {
    param([string]$Root,[string]$Path)
    $rootFull=Assert-BFSafePath $Root
    $candidate=[IO.Path]::GetFullPath($Path)
    $prefix=$rootFull.TrimEnd([IO.Path]::DirectorySeparatorChar,[IO.Path]::AltDirectorySeparatorChar)+[IO.Path]::DirectorySeparatorChar
    return $candidate.StartsWith($prefix,[StringComparison]::OrdinalIgnoreCase)
}

function Get-BFArchitectureAnchor {
    # GitHub-style heading anchor: lowercase, drop punctuation, spaces to hyphens.
    param([Parameter(Mandatory)][string]$Heading)
    $builder=[Text.StringBuilder]::new()
    foreach($character in $Heading.ToLowerInvariant().ToCharArray()){
        if([char]::IsLetterOrDigit($character) -or $character -eq '-' -or $character -eq '_' -or $character -eq ' '){[void]$builder.Append($character)}
    }
    return (($builder.ToString() -replace '\s+','-').Trim('-'))
}

function Get-BFADRSections {
    # Parse the normative source once per content hash. The index still owns the
    # links; excerpts are read from here, never copied into the index.
    param([Parameter(Mandatory)][string]$SourcePath)
    $path=Assert-BFSafePath $SourcePath
    if(-not (Test-Path -LiteralPath $path -PathType Leaf)){throw (New-BFError 'BF_INVALID' ("ADR source file is missing: {0}." -f $path))}
    $bytes=[IO.File]::ReadAllBytes($path)
    $contentHash=([BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash($bytes))).Replace('-','').ToLowerInvariant()
    $cacheKey=$path+'|'+$contentHash
    if($script:BFADRSectionCache.ContainsKey($cacheKey)){return $script:BFADRSectionCache[$cacheKey]}
    $text=[Text.Encoding]::UTF8.GetString($bytes)
    $sections=[ordered]@{}
    foreach($match in [regex]::Matches($text,'(?ms)^##[ \t]+ADR-([0-9]+):[ \t]*(.+?)[ \t]*\r?\n(?<body>.*?)(?=^##[ \t]|\z)')){
        $id='ADR-'+[int]$match.Groups[1].Value
        if($sections.Contains($id)){throw (New-BFError 'BF_INVALID' ("Duplicate ADR heading: {0}." -f $id))}
        $title=$match.Groups[2].Value
        $sections[$id]=[ordered]@{title=$title;anchor=(Get-BFArchitectureAnchor ("${id}: ${title}"));body=$match.Groups['body'].Value}
    }
    $script:BFADRSectionCache[$cacheKey]=$sections
    return $sections
}

function Get-BFADRHeadings {
    param([Parameter(Mandatory)][string]$SourcePath)
    $projected=[ordered]@{}
    foreach($entry in (Get-BFADRSections $SourcePath).GetEnumerator()){
        $projected[$entry.Key]=[ordered]@{title=$entry.Value.title;anchor=$entry.Value.anchor}
    }
    return $projected
}

function Get-BFADRExcerpt {
    param([Parameter(Mandatory)][string]$Body,[int]$MaxChars=500)
    $match=[regex]::Match($Body,'(?s)\*\*Решение\.\*\*\s*(?<text>.+?)(?=\r?\n\s*\*\*|\z)')
    $text=if($match.Success){$match.Groups['text'].Value}else{$Body}
    $text=(($text -replace '\s+',' ').Trim())
    if($text.Length -gt $MaxChars){$text=$text.Substring(0,$MaxChars).TrimEnd()+'...'}
    return $text
}

function Get-BFStageSubjects {
    # Minimal explicit subject set per stage. Unknown stages get no bundle.
    param([Parameter(Mandatory)][string]$Stage)
    $subjects=switch($Stage){
        'inspect'        {@('controller.state','controller.gates')}
        'spec'           {@('controller.gates')}
        'spec_review'    {@('controller.gates','adapter.codex','adapter.opencode')}
        'spec_reconcile' {@('controller.gates','adapter.codex','adapter.opencode')}
        'implement'      {@('controller.gates','controller.execution')}
        'code_review'    {@('controller.gates','adapter.codex','adapter.opencode')}
        'code_reconcile' {@('controller.gates','adapter.codex','adapter.opencode')}
        'verify'         {@('controller.gates','runtime.native-1c')}
        'diagnose'       {@('controller.recovery','controller.gates')}
        'acceptance'     {@('controller.gates','publication.git')}
        default          {@()}
    }
    return [string[]]@($subjects)
}

function Get-BFArchitectureRenderedLength {
    # Measure the exact prompt text, including titles, paths, anchors and the
    # bounded excluded summary, not only the excerpts. The excluded sample is
    # bounded so a long excluded list cannot defeat the char limit.
    param($Entries,$Missing,$Excluded)
    $sample=@($Excluded | Select-Object -First $script:BFArchitectureBundleMaxExcluded)
    $temp=[ordered]@{decisions=@($Entries);missing_context=@($Missing);excluded=$sample;excluded_count=@($Excluded).Count}
    return (Format-BFArchitectureBundlePrompt $temp).Length
}

function Get-BFArchitectureBundle {
    # Deterministic, size-bounded selection of accepted ADRs for one stage.
    # A missing index degrades to missing_context; a damaged index fails closed.
    # Identity covers ALL applicable decisions (including ones omitted from the
    # presentation), so any applicable ADR change invalidates bound evidence.
    param([Parameter(Mandatory)][string]$Stage,[string]$RepositoryRoot)
    $root = if([string]::IsNullOrWhiteSpace($RepositoryRoot)){Get-BFArchitectureRoot}else{Assert-BFSafePath $RepositoryRoot}
    $subjects=[string[]]@(Get-BFStageSubjects $Stage)
    $missing=@();$excluded=@();$records=@();$subjectRefs=@()
    $index=$null
    if(-not (Test-Path -LiteralPath (Get-BFArchitectureIndexPath $root) -PathType Leaf)){
        # Absent index degrades to missing_context; a damaged index must throw.
        $missing+='adr-index'
    } else {
        $index=Read-BFArchitectureIndex $root
        Assert-BFADRIndex $index $root | Out-Null
    }
    if($null -ne $index){
        foreach($subject in $subjects){
            $definitions=@($index.subjects | Where-Object { $_.id -ceq $subject })
            if($definitions.Count -gt 0){$subjectRefs+=[ordered]@{id=[string]$definitions[0].id;kind=[string]$definitions[0].kind;refs=@($definitions[0].refs)}}
            else{$subjectRefs+=[ordered]@{id=$subject;kind='missing';refs=@()};$missing+=('subject:'+$subject)}
        }
    }
    if($null -ne $index -and $subjects.Count -gt 0){
        $applicable=@(Get-BFArchitectureApplicableDecisions $index $subjects)
        $sectionsBySource=@{}
        foreach($decision in $applicable){
            $id=[string]$decision.id
            $sourcePath=Assert-BFSafePath (Join-Path $root ([string]$decision.source.path))
            if(-not $sectionsBySource.ContainsKey($sourcePath)){$sectionsBySource[$sourcePath]=Get-BFADRSections $sourcePath}
            $sections=$sectionsBySource[$sourcePath]
            $body=if($sections.Contains($id)){[string]$sections[$id].body}else{''}
            $excerpt=if([string]::IsNullOrWhiteSpace($body)){''}else{Get-BFADRExcerpt $body $script:BFArchitectureBundleMaxExcerpt}
            $records+=[ordered]@{id=$id;title=[string]$decision.title;status=[string]$decision.status;source=[ordered]@{path=[string]$decision.source.path;anchor=[string]$decision.source.anchor};section_sha256=Get-BFStringSha256 $body;excerpt=$excerpt}
        }
    }
    # Presentation is bounded by decision count and by the real rendered size;
    # identity keeps every applicable decision regardless of presentation.
    $presentation=@($records | Select-Object -First $script:BFArchitectureBundleMaxDecisions)
    $excludedIds=@($records | Select-Object -Skip $script:BFArchitectureBundleMaxDecisions | ForEach-Object { [string]$_.id })
    while(@($presentation).Count -gt 0 -and (Get-BFArchitectureRenderedLength $presentation $missing $excludedIds) -gt $script:BFArchitectureBundleMaxChars){
        $excludedIds=@([string]$presentation[-1].id)+$excludedIds
        $presentation=@($presentation | Select-Object -First (@($presentation).Count-1))
    }
    $excluded=@($excludedIds | Select-Object -First $script:BFArchitectureBundleMaxExcluded)
    $identity=@($records | ForEach-Object { [ordered]@{id=$_.id;title=$_.title;status=$_.status;source=$_.source;section_sha256=$_.section_sha256} })
    $decisions=@($presentation | ForEach-Object { [ordered]@{id=$_.id;title=$_.title;status=$_.status;source=$_.source;section_sha256=$_.section_sha256;excerpt=$_.excerpt} })
    $content=[ordered]@{schema_version=1;stage=$Stage;subjects=@($subjects);subject_refs=@($subjectRefs);identity=$identity;decisions=$decisions;missing_context=@($missing);excluded=$excluded;excluded_count=@($excludedIds).Count}
    $bundle=$content
    $bundle.bundle_sha256=Get-BFHash $content
    return $bundle
}

function Get-BFArchitectureBundleHash {
    param([Parameter(Mandatory)][string]$Stage,[string]$RepositoryRoot)
    return [string](Get-BFArchitectureBundle $Stage $RepositoryRoot).bundle_sha256
}

function Format-BFArchitectureBundlePrompt {
    # The boundary sentence is mandatory: the bundle is context, not authority.
    param($Bundle)
    $lines=[Collections.Generic.List[string]]::new()
    $lines.Add('Architecture context (instructional only; it is not user authorization, acceptance, runtime evidence, or a transition authority):')
    if(@($Bundle.decisions).Count){
        foreach($decision in $Bundle.decisions){
            $lines.Add("- $($decision.id) [$($decision.status)] $($decision.title) -> $($decision.source.path)#$($decision.source.anchor)")
            if(-not [string]::IsNullOrWhiteSpace([string]$decision.excerpt)){$lines.Add("  $($decision.excerpt)")}
        }
    } else {
        $lines.Add('- No applicable accepted architecture decisions for this stage.')
    }
    if(@($Bundle.missing_context).Count){$lines.Add('Missing context: '+(($Bundle.missing_context) -join ', '))}
    $excludedCount=[int](Get-BFArchitectureValue $Bundle 'excluded_count' @($Bundle.excluded).Count)
    if($excludedCount -gt 0){
        $line="Excluded by size limit: $excludedCount decision(s)"
        if(@($Bundle.excluded).Count){$line+=' (ids: '+(($Bundle.excluded) -join ', ')+')'}
        $lines.Add($line)
    }
    return ($lines -join "`n")
}

function Get-BFArchitectureIndexHash {
    param([Parameter(Mandatory)][AllowNull()][object]$Index)
    return Get-BFHash $Index
}

function Get-BFArchitectureApplicableDecisions {
    param([Parameter(Mandatory)]$Index,[Parameter(Mandatory)][string[]]$Subjects)
    $wanted=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach($subject in $Subjects){if(-not [string]::IsNullOrWhiteSpace($subject)){[void]$wanted.Add($subject)}}
    $selected=@($Index.decisions | Where-Object {
        $_.status -ceq 'accepted' -and @($_.applies_to | Where-Object { $wanted.Contains([string]$_) }).Count -gt 0
    })
    return @($selected | Sort-Object -Property id -CaseSensitive)
}

function Assert-BFADRIndex {
    param([Parameter(Mandatory)]$Index,[string]$RepositoryRoot)
    $root = if([string]::IsNullOrWhiteSpace($RepositoryRoot)){Get-BFArchitectureRoot}else{Assert-BFSafePath $RepositoryRoot}
    $schemaPath=Join-Path $root 'docs/architecture/adr-index.schema.json'
    if(-not (Test-Path -LiteralPath $schemaPath -PathType Leaf)){throw (New-BFError 'BF_INVALID' 'ADR index schema is missing.')}
    $canonical=Get-BFCanonicalJson $Index
    try {
        if(-not (Test-Json -Json $canonical -SchemaFile $schemaPath -ErrorAction Stop)){throw 'schema mismatch'}
    } catch {
        throw (New-BFError 'BF_INVALID' ("ADR index does not match its schema: {0}" -f $_.Exception.Message))
    }

    $rootFull=Assert-BFSafePath $root
    $packageFull=Assert-BFSafePath (Get-BFArchitectureRoot)
    $subjectIds=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach($subject in $Index.subjects){
        if(-not $subjectIds.Add([string]$subject.id)){throw (New-BFError 'BF_INVALID' ("Duplicate architecture subject: {0}." -f $subject.id))}
        foreach($reference in @($subject.refs)){
            Assert-BFArchitectureRelativePath ([string]$reference)
            $parts=[string]$reference -split '#',2
            $relativePath=$parts[0]
            $symbol=if($parts.Count -gt 1){$parts[1]}else{$null}
            # Subject references describe the framework package; resolve them in
            # the architecture root first, then the framework package root.
            $candidate=$null
            foreach($base in @($rootFull,$packageFull)){
                $probe=[IO.Path]::GetFullPath((Join-Path $base $relativePath))
                if(-not (Test-BFPathContained $base $probe)){throw (New-BFError 'BF_INVALID' ("Architecture subject reference escapes its root: {0}." -f $reference))}
                if(Test-Path -LiteralPath $probe -PathType Leaf){$candidate=$probe;break}
            }
            if($null -eq $candidate){throw (New-BFError 'BF_INVALID' ("Architecture subject reference is missing: {0}." -f $reference))}
            if(-not [string]::IsNullOrWhiteSpace([string]$symbol)){
                if(-not ([IO.File]::ReadAllText($candidate)).Contains([string]$symbol)){throw (New-BFError 'BF_INVALID' ("Architecture subject symbol is missing: {0}." -f $reference))}
            }
        }
    }
    $known=[ordered]@{}
    $headingsBySource=[ordered]@{}
    foreach($decision in $Index.decisions){
        $id=[string]$decision.id
        if($known.Contains($id)){throw (New-BFError 'BF_INVALID' ("Duplicate ADR id: {0}." -f $id))}
        if($id -cnotmatch '^ADR-[0-9]+$'){throw (New-BFError 'BF_INVALID' ("ADR id must look like ADR-<number>: {0}." -f $id))}
        foreach($subject in $decision.applies_to){
            if(-not $subjectIds.Contains([string]$subject)){throw (New-BFError 'BF_INVALID' ("Unknown architecture subject {0} on {1}." -f $subject,$id))}
        }
        Assert-BFArchitectureRelativePath ([string]$decision.source.path)
        $sourcePath=[IO.Path]::GetFullPath((Join-Path $rootFull ([string]$decision.source.path)))
        if(-not (Test-BFPathContained $rootFull $sourcePath)){throw (New-BFError 'BF_INVALID' ("ADR source escapes the architecture root for {0}: {1}." -f $id,$decision.source.path))}
        if(-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)){throw (New-BFError 'BF_INVALID' ("ADR source is missing for {0}: {1}." -f $id,$decision.source.path))}
        if(-not $headingsBySource.Contains($sourcePath)){$headingsBySource[$sourcePath]=Get-BFADRHeadings $sourcePath}
        $headings=$headingsBySource[$sourcePath]
        if(-not $headings.Contains($id)){throw (New-BFError 'BF_INVALID' ("ADR heading is missing for {0} in {1}." -f $id,$decision.source.path))}
        if([string]$decision.source.anchor -cne [string]$headings[$id].anchor){throw (New-BFError 'BF_INVALID' ("ADR anchor for {0} does not match the source heading." -f $id))}
        if([string]$decision.title -cne [string]$headings[$id].title){throw (New-BFError 'BF_INVALID' ("ADR title for {0} differs from the normative source heading." -f $id))}
        foreach($field in @('informed_by','supersedes')){
            foreach($reference in @(Get-BFArchitectureValue $decision $field @())){
                if([string]$reference -ceq $id){throw (New-BFError 'BF_INVALID' ("{0} references itself in {1}." -f $id,$field))}
            }
        }
        $known[$id]=$decision
    }

    foreach($decision in $Index.decisions){
        $id=[string]$decision.id
        foreach($field in @('informed_by','supersedes')){
            foreach($reference in @(Get-BFArchitectureValue $decision $field @())){
                if(-not $known.Contains([string]$reference)){throw (New-BFError 'BF_INVALID' ("{0} has an invalid {1} reference: {2}." -f $id,$field,$reference))}
            }
        }
    }

    # DFS cycle detection over supersedes runs before lineage checks so a cycle
    # is reported as a cycle rather than as an unrelated status mismatch.
    $state=[Collections.Generic.Dictionary[string,int]]::new([StringComparer]::Ordinal)
    foreach($id in $known.Keys){$state[$id]=0}
    $visit=$null;$visit={
        param([string]$Id)
        if($state[$Id] -eq 1){throw (New-BFError 'BF_INVALID' ("Supersedes cycle at {0}." -f $Id))}
        if($state[$Id] -eq 2){return}
        $state[$Id]=1
        foreach($target in @(Get-BFArchitectureValue $known[$Id] 'supersedes' @())){& $visit ([string]$target)}
        $state[$Id]=2
    }
    foreach($id in $known.Keys){& $visit $id}

    foreach($decision in $Index.decisions){
        $id=[string]$decision.id
        foreach($target in @(Get-BFArchitectureValue $decision 'supersedes' @())){
            if([string]$known[[string]$target].status -cne 'superseded'){throw (New-BFError 'BF_INVALID' ("{0} supersedes {1}, but {1} is not marked superseded." -f $id,$target))}
        }
        if([string]$decision.status -ceq 'superseded'){
            $superseder=@($Index.decisions | Where-Object { $_.status -ceq 'accepted' -and @(Get-BFArchitectureValue $_ 'supersedes' @()) -contains $id })
            if($superseder.Count -ne 1){throw (New-BFError 'BF_INVALID' ("Superseded {0} requires exactly one accepted superseding decision." -f $id))}
        }
    }
    return $Index
}

function Read-BFArchitectureIndex {
    param([string]$RepositoryRoot)
    $path=Get-BFArchitectureIndexPath $RepositoryRoot
    if(-not (Test-Path -LiteralPath $path -PathType Leaf)){throw (New-BFError 'BF_INVALID' ("ADR index is missing: {0}." -f $path))}
    return Read-BFJson $path
}

function Assert-BFArchitectureIndexFile {
    param([string]$RepositoryRoot,[switch]$PassThru)
    $root = if([string]::IsNullOrWhiteSpace($RepositoryRoot)){Get-BFArchitectureRoot}else{Assert-BFSafePath $RepositoryRoot}
    $index=Read-BFArchitectureIndex $root
    Assert-BFADRIndex $index $root | Out-Null
    if($PassThru){return $index}
}

function Get-BFPackageIdentity {
    # A real package identity, not a per-file guess: the hashed package manifest
    # when present, otherwise the executing host/entrypoint hash, with its
    # source named explicitly.
    param($State)
    $version=''
    $versionPath=Join-Path (Get-BFArchitectureRoot) 'VERSION'
    if(Test-Path -LiteralPath $versionPath -PathType Leaf){$version=([IO.File]::ReadAllText($versionPath)).Trim()}
    $manifestPath=Join-Path (Get-BFArchitectureRoot) 'package-manifest.json'
    if(Test-Path -LiteralPath $manifestPath -PathType Leaf){
        return [ordered]@{version=$version;sha256=(Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256).Hash.ToLowerInvariant();source='manifest'}
    }
    $files=@(Get-BFArchitectureValue $State 'policy_files' @())
    $hostMatches=@($files | Where-Object { ([string](Get-BFArchitectureValue $_ 'path' '')) -match '(?i)bsl-flow\.exe$' })
    if($hostMatches.Count -gt 0){return [ordered]@{version=$version;sha256=[string](Get-BFArchitectureValue $hostMatches[0] 'sha256' '');source='host'}}
    $entryMatches=@($files | Where-Object { ([string](Get-BFArchitectureValue $_ 'path' '')) -match '(?i)Invoke-BSLFlowTask\.ps1$' })
    if($entryMatches.Count -gt 0){return [ordered]@{version=$version;sha256=[string](Get-BFArchitectureValue $entryMatches[0] 'sha256' '');source='entrypoint'}}
    return [ordered]@{version=$version;sha256='';source=''}
}

function Get-BFLastAttemptReceipts {
    # The last terminal attempt, not merely the most recently started one.
    param($State)
    $attempts=@(Get-BFArchitectureValue $State 'attempts' @())
    if($attempts.Count -eq 0){return $null}
    $projectPath=[string](Get-BFArchitectureValue $State 'project_path' '')
    if([string]::IsNullOrWhiteSpace($projectPath)){return [ordered]@{attempt_id=[string]$attempts[-1];stage=$null;outcome='unknown';receipts=@()}}
    $taskDirectory=Get-BFArchitectureTaskDirectory $projectPath $State.task_id
    $attemptId=$null
    for($index=$attempts.Count-1;$index -ge 0;$index--){
        $candidate=Join-Path $taskDirectory ('attempts/'+[string]$attempts[$index])
        if(Test-Path -LiteralPath (Join-Path $candidate 'result.json') -PathType Leaf){$attemptId=[string]$attempts[$index];break}
    }
    if($null -eq $attemptId){$attemptId=[string]$attempts[-1]}
    $attemptDirectory=Join-Path $taskDirectory ('attempts/'+$attemptId)
    $stage=$null;$outcome='pending'
    $resultPath=Join-Path $attemptDirectory 'result.json'
    if(Test-Path -LiteralPath $resultPath -PathType Leaf){
        $result=Read-BFJson $resultPath
        $stage=[string](Get-BFArchitectureValue $result 'stage' '')
        $outcome=[string](Get-BFArchitectureValue $result 'outcome' 'unknown')
    }
    $receipts=@()
    foreach($relative in @('start.json','result.json','failure.json','raw/worker/exit.json','raw/worker/host-result.json','raw/worker/model-result.json')){
        $receiptPath=Join-Path $attemptDirectory $relative
        if(Test-Path -LiteralPath $receiptPath -PathType Leaf){
            $receipts+=[ordered]@{name=$relative;path=$receiptPath;sha256=(Get-FileHash -LiteralPath $receiptPath -Algorithm SHA256).Hash.ToLowerInvariant()}
        }
    }
    return [ordered]@{attempt_id=$attemptId;stage=$stage;outcome=$outcome;receipts=$receipts}
}

function New-BFContextErrorEnvelope {
    # A read error must still return a versioned context-shaped envelope and
    # preserve the useful resume context, not a contradictory empty projection.
    param($State,[string]$Message,[string]$ProjectPath,[string]$FallbackRoot)
    $stateHash='';try{$stateHash=Get-BFHash $State}catch{$stateHash=''}
    $resolved=[ordered]@{root='';scope='error'}
    try{$resolved=Resolve-BFArchitectureContext $ProjectPath $FallbackRoot}catch{}
    $package=Get-BFPackageIdentity $State
    $evidence=@()
    foreach($entry in @(Get-BFArchitectureValue $State 'evidence' @())){
        $evidence+=[ordered]@{attempt_id=[string](Get-BFArchitectureValue $entry 'attempt_id' '');stage=[string](Get-BFArchitectureValue $entry 'stage' '');outcome=[string](Get-BFArchitectureValue $entry 'outcome' 'unknown');fresh=$false;reason=$Message}
    }
    $lastAttempt=$null;try{$lastAttempt=Get-BFLastAttemptReceipts $State}catch{$lastAttempt=$null}
    return [ordered]@{
        schema_version=1
        task_id=[string](Get-BFArchitectureValue $State 'task_id' '');revision=[int](Get-BFArchitectureValue $State 'revision' 1);status='blocked';stage=[string](Get-BFArchitectureValue $State 'stage' 'inspect')
        intent_hash=[string](Get-BFArchitectureValue $State 'intent_hash' '');policy_hash=[string](Get-BFArchitectureValue $State 'policy_hash' '');baseline=[string](Get-BFArchitectureValue $State 'baseline' '')
        classification=Get-BFArchitectureValue $State 'classification'
        active_attempt=Get-BFArchitectureValue $State 'active_attempt'
        unresolved_effect=Get-BFArchitectureValue $State 'unresolved_effect'
        question=Get-BFArchitectureValue $State 'question'
        next=[ordered]@{action='inspect_blocker';stage=[string](Get-BFArchitectureValue $State 'stage' 'inspect');blockers=@($Message);reason=$Message}
        evidence=$evidence
        last_attempt=$lastAttempt
        generated_from=[ordered]@{state_sha256=$stateHash;policy_sha256=[string](Get-BFArchitectureValue $State 'policy_hash' '');source_sha256='';adr_index_sha256='error';adr_index_path='docs/architecture/adr-index.json';adr_root=[string]$resolved.root;adr_scope=[string]$resolved.scope;package_version=$package.version;package_sha256=$package.sha256;package_source=$package.source}
        missing_context=@();stale_context=@()
    }
}

function Get-BFTaskContext {
    # Read-only resume projection. It includes the authoritative Get-BFNext
    # result and never creates a transition, authorization or acceptance.
    param([Parameter(Mandatory)]$State,[string]$ProjectPath,[AllowNull()]$Next,[string]$FallbackRoot)
    $resolved=Resolve-BFArchitectureContext $ProjectPath $FallbackRoot
    $root=[string]$resolved.root
    $missing=@()
    $adrHash=$null
    if(-not (Test-Path -LiteralPath (Get-BFArchitectureIndexPath $root) -PathType Leaf)){
        $missing+='adr-index';$adrHash='missing'
    } else {
        # Present-but-damaged must surface as a read error, never as missing.
        $index=Read-BFArchitectureIndex $root
        Assert-BFADRIndex $index $root | Out-Null
        $adrHash=Get-BFArchitectureIndexHash $index
    }
    if($null -eq $Next){
        try { $Next=Get-BFNext $State }
        catch { $Next=[ordered]@{stage=[string](Get-BFArchitectureValue $State 'stage' 'inspect');action='inspect_blocker';blockers=@($_.Exception.Message)} }
    }
    $evidence=@()
    $stale=@()
    foreach($entry in @($State.evidence)){
        $fresh=$false;$reason=$null
        try { $fresh=[bool](Test-BFEvidenceFresh $State $entry $null) }
        catch { $reason=$_.Exception.Message }
        if(-not $fresh){$stale+=[string]$entry.attempt_id}
        $evidence+=[ordered]@{attempt_id=$entry.attempt_id;stage=$entry.stage;outcome=$entry.outcome;fresh=$fresh;reason=$reason}
    }
    $sourceSha=''
    if(-not [string]::IsNullOrWhiteSpace([string](Get-BFArchitectureValue $State 'project_path' ''))){
        try{$sourceSha=(Get-BFSourceManifest $State).sha256}catch{$sourceSha='';$missing+='source-manifest'}
    }
    $lastAttempt=$null
    try{$lastAttempt=Get-BFLastAttemptReceipts $State}catch{$lastAttempt=$null}
    if($null -eq $lastAttempt -and @($State.evidence).Count){
        $lastEvidence=$State.evidence[-1]
        $lastAttempt=[ordered]@{attempt_id=[string]$lastEvidence.attempt_id;stage=[string]$lastEvidence.stage;outcome=[string]$lastEvidence.outcome;receipts=@()}
    }
    $package=Get-BFPackageIdentity $State
    $reason=switch([string]$Next.action){
        'accept' {'All required evidence is fresh; controller acceptance is the next authorized step.'}
        'dispatch' {"Controller dispatch of stage '$($Next.stage)' is the next authorized step."}
        'recover' {'An unfinished or uncertain attempt must be reconciled before any new dispatch.'}
        'needs_input' {'A trusted operator answer is required before dispatch.'}
        'blocked' {'The task is blocked; no transition is authorized.'}
        'failed' {'The task failed; a trusted update is required.'}
        'cancelled' {'The task is cancelled; a trusted update is required.'}
        default {''}
    }
    return [ordered]@{
        schema_version=1
        task_id=$State.task_id; revision=$State.revision; status=$State.status; stage=$State.stage
        intent_hash=$State.intent_hash; policy_hash=$State.policy_hash; baseline=$State.baseline
        classification=$State.classification
        active_attempt=$State.active_attempt
        unresolved_effect=$State.unresolved_effect
        question=$State.question
        next=[ordered]@{action=[string](Get-BFArchitectureValue $Next 'action' 'inspect_blocker');stage=[string](Get-BFArchitectureValue $Next 'stage' '');blockers=@(Get-BFArchitectureValue $Next 'blockers' @());reason=$reason}
        evidence=$evidence
        last_attempt=$lastAttempt
        generated_from=[ordered]@{state_sha256=(Get-BFHash $State);policy_sha256=$State.policy_hash;source_sha256=$sourceSha;adr_index_sha256=$adrHash;adr_index_path='docs/architecture/adr-index.json';adr_root=$root;adr_scope=[string]$resolved.scope;package_version=$package.version;package_sha256=$package.sha256;package_source=$package.source}
        missing_context=@($missing)
        stale_context=@($stale)
        memory=(Get-BFMemoryProjection $State $Next)
    }
}
