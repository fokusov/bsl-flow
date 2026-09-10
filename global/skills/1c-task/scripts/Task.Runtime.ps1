#Requires -Version 7.0
Set-StrictMode -Version Latest

function Get-BFRuntimeTargetKey {
    param([Parameter(Mandatory)][string]$Target)
    $canonical=(Get-BFRuntimeTargetIdentity $Target).ToLowerInvariant()
    $bytes=[Text.Encoding]::UTF8.GetBytes($canonical)
    try { return ([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes))).ToLowerInvariant() }
    finally { [Array]::Clear($bytes,0,$bytes.Length) }
}

function Get-BFRuntimeTargetIdentity {
    param([Parameter(Mandatory)][string]$Target)
    if (-not [IO.Path]::IsPathRooted($Target)) { throw 'BF_INVALID: native 1C target must be an absolute FILE directory.' }
    $marker=Join-Path ([IO.Path]::GetFullPath($Target).TrimEnd('\','/')) '1Cv8.1CD'
    if(-not(Test-Path -LiteralPath $marker -PathType Leaf)){throw 'BF_BLOCKED: FILE target marker is required to resolve physical target identity.'}
    if($null -eq ('BFNativePath' -as [type])){Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
public static class BFNativePath {
  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
  public static extern uint GetFinalPathNameByHandle(SafeFileHandle hFile, System.Text.StringBuilder path, uint size, uint flags);
}
'@}
    $handle=[IO.File]::OpenHandle($marker,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete)
    try{$buffer=[Text.StringBuilder]::new(32768);$length=[BFNativePath]::GetFinalPathNameByHandle($handle,$buffer,[uint32]$buffer.Capacity,0);if($length -eq 0 -or $length -ge $buffer.Capacity){throw 'BF_BLOCKED: physical FILE target identity could not be resolved.'};$physical=$buffer.ToString();if($physical.StartsWith('\\?\UNC\',[StringComparison]::OrdinalIgnoreCase)){throw 'BF_BLOCKED: UNC FILE targets are not supported by the first native adapter.'};if($physical.StartsWith('\\?\')){$physical=$physical.Substring(4)};$resolved=Split-Path $physical -Parent;if([IO.Path]::GetFullPath($Target).TrimEnd('\') -ine $resolved){throw 'BF_BLOCKED: aliased FILE target paths are not admitted; use the resolved physical path.'};return $resolved}finally{$handle.Dispose()}
}

function Assert-BFNativeCriterion {
    param($Criterion)
    $native=Get-BFValue $Criterion 'native_1c'
    if($null -eq $native){throw 'BF_INVALID: native_1c contract is required.'}
    if($Criterion.kind -ne 'integration'){throw 'BF_INVALID: native_1c is supported only for integration criteria.'}
    Assert-BFFields $native @('source_root','extension','module','platform_version','executable_sha256','authorized_operations','authorization_reference') @('reuse_load_attempt') 'criterion.native_1c'
    Assert-BFRelativePath $native.source_root
    foreach($name in @('extension','module','platform_version','authorization_reference')){Assert-BFText $native.$name ('criterion.native_1c.'+$name)}
    if($native.extension -cnotmatch '^[A-Za-zА-Яа-яЁё_][A-Za-zА-Яа-яЁё0-9_]{0,127}$' -or $native.module -cnotmatch '^[A-Za-zА-Яа-яЁё_][A-Za-zА-Яа-яЁё0-9_]{0,127}$'){throw 'BF_INVALID: unsafe native 1C extension or module name.'}
    if($native.platform_version -cnotmatch '^8\.3\.\d+\.\d+$'){throw 'BF_INVALID: invalid native 1C platform version.'}
    if($native.executable_sha256 -cnotmatch '^[0-9a-f]{64}$'){throw 'BF_INVALID: native executable SHA-256 must be lowercase hexadecimal.'}
    if($Criterion.executable -isnot [string] -or -not [IO.Path]::IsPathRooted($Criterion.executable) -or [IO.Path]::GetFileName($Criterion.executable) -ine '1cv8.exe'){throw 'BF_INVALID: native executable must be an absolute 1cv8.exe path.'}
    if($Criterion.arguments -isnot [array] -or $Criterion.arguments.Count -ne 0){throw 'BF_INVALID: native 1C criteria do not accept free arguments.'}
    if($Criterion.protected_paths -isnot [array] -or $Criterion.protected_paths.Count -eq 0){throw 'BF_INVALID: native 1C criteria require protected_paths for declared tests and fixtures.'}
    foreach($path in $Criterion.protected_paths){Assert-BFRelativePath $path}
    if(-not [IO.Path]::IsPathRooted($Criterion.target)){throw 'BF_INVALID: native 1C target must be an absolute FILE directory.'}
    [void](Get-BFRuntimeTargetKey $Criterion.target)
    $operations='inventory,load,update,test'
    if($null -ne (Get-BFValue $native 'reuse_load_attempt')){Assert-BFUuid $native.reuse_load_attempt;$operations='inventory,test'}
    if($native.authorized_operations -isnot [array] -or (@($native.authorized_operations) -join ',') -cne $operations){throw ('BF_INVALID: authorized_operations must be exactly '+$operations+'.')}
    if($Criterion.expected_tests -isnot [array] -or $Criterion.expected_tests.Count -eq 0 -or @($Criterion.expected_tests|Select-Object -Unique).Count -ne $Criterion.expected_tests.Count){throw 'BF_INVALID: unique class-qualified expected tests are required.'}
    foreach($id in $Criterion.expected_tests){if($id -isnot [string] -or $id -cnotmatch '^[^\.\s]+(?:\.[^\.\s]+)+$'){throw 'BF_INVALID: expected native test IDs must be classname.name.'}}
}

function Get-BFNativeSource {
    param([Parameter(Mandatory)][string]$Root)
    $safe=Assert-BFSafePath $Root
    if(-not(Test-Path -LiteralPath $safe -PathType Container)){throw 'BF_BLOCKED: native source root is missing.'}
    $rows=@(Get-ChildItem -LiteralPath $safe -File -Recurse -Force | ForEach-Object {
        [void](Assert-BFSafePath $_.FullName)
        [ordered]@{path=$_.FullName.Substring($safe.Length).TrimStart('\','/').Replace('\','/');sha256=Get-BFFileHash $_.FullName}
    } | Sort-Object path -CaseSensitive)
    if($rows.Count -eq 0){throw 'BF_BLOCKED: native source snapshot would be empty.'}
    $config=Join-Path $safe 'Configuration.xml'
    if(-not(Test-Path -LiteralPath $config -PathType Leaf)){throw 'BF_BLOCKED: Configuration.xml is missing.'}
    $settings=[Xml.XmlReaderSettings]::new();$settings.DtdProcessing=[Xml.DtdProcessing]::Prohibit;$settings.XmlResolver=$null
    $reader=[Xml.XmlReader]::Create($config,$settings)
    try{$xml=[Xml.XmlDocument]::new();$xml.XmlResolver=$null;$xml.Load($reader)}finally{$reader.Dispose()}
    $node=$xml.SelectSingleNode('/*[local-name()="MetaDataObject"]/*[local-name()="Configuration"]')
    if($null -eq $node){throw 'BF_BLOCKED: extension Configuration node is missing.'}
    $name=$node.SelectSingleNode('*[local-name()="Properties"]/*[local-name()="Name"]').InnerText
    $version=$node.SelectSingleNode('*[local-name()="Properties"]/*[local-name()="Version"]').InnerText
    $uuid=$node.GetAttribute('uuid').ToLowerInvariant()
    if($uuid -cnotmatch '^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$' -or $uuid -ceq '00000000-0000-0000-0000-000000000000'){throw 'BF_BLOCKED: extension UUID is missing or zero.'}
    $owned=[Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach($file in Get-ChildItem -LiteralPath $safe -File -Recurse -Filter '*.xml'){
        if($file.Name -eq 'ConfigDumpInfo.xml'){continue}
        $metadataReader=[Xml.XmlReader]::Create($file.FullName,$settings)
        try{$metadata=[Xml.XmlDocument]::new();$metadata.XmlResolver=$null;$metadata.Load($metadataReader)}finally{$metadataReader.Dispose()}
        # ClassId and borrowed-object references are not owned identities.
        $identities=@($metadata.SelectNodes('//@uuid | //*[local-name()="ContainedObject"]/*[local-name()="ObjectId"]'))
        foreach($identity in $identities){
            $id=$identity.get_InnerText().ToLowerInvariant()
            if($id -cnotmatch '^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$' -or $id.StartsWith('00000000-0000-0000-0000-') -or -not $owned.Add($id)){throw 'BF_BLOCKED: owned metadata UUIDs must be unique and free of scaffold placeholders.'}
        }
        foreach($reference in @($metadata.SelectNodes('//*[local-name()="ExtendedConfigurationObject"]'))){
            if($reference.InnerText.Trim() -ceq '00000000-0000-0000-0000-000000000000'){throw 'BF_BLOCKED: ExtendedConfigurationObject UUID is zero.'}
        }
    }
    return [ordered]@{root=$safe;extension=$name;version=$version;uuid=$uuid;files=$rows;sha256=Get-BFHash $rows}
}

function Get-BFNativeDependencies {
    param($Criterion)
    Assert-BFNativeCriterion $Criterion
    $target=Get-BFRuntimeTargetIdentity $Criterion.target
    $actualExecutableHash=Get-BFFileHash $Criterion.executable
    if($actualExecutableHash -cne $Criterion.native_1c.executable_sha256){throw 'BF_BLOCKED: authorized 1cv8.exe identity changed.'}
    $version=(Get-Item -LiteralPath $Criterion.executable).VersionInfo.FileVersion
    if($version -cne $Criterion.native_1c.platform_version){throw 'BF_BLOCKED: actual 1cv8.exe version differs from the criterion.'}
    $comDll=Join-Path (Split-Path $Criterion.executable -Parent) 'comcntr.dll'
    if(-not(Test-Path -LiteralPath $comDll -PathType Leaf)){throw 'BF_BLOCKED: platform COM connector is missing.'}
    return [ordered]@{
        criterion_id=$Criterion.id
        target_key=Get-BFRuntimeTargetKey $target
        target=$target
        platform_version=$version
        executable_sha256=$actualExecutableHash
        com_connector_sha256=Get-BFFileHash $comDll
        expected_tests=@($Criterion.expected_tests)
        authorization_reference=$Criterion.native_1c.authorization_reference
    }
}

function Copy-BFNativeSnapshot {
    param($Source,[string]$Destination)
    [void][IO.Directory]::CreateDirectory((Assert-BFSafePath $Destination))
    foreach($file in $Source.files){$to=Assert-BFSafePath (Join-Path $Destination $file.path);[void][IO.Directory]::CreateDirectory((Split-Path $to -Parent));[IO.File]::Copy((Join-Path $Source.root $file.path),$to,$false)}
    $copy=Get-BFNativeSource $Destination
    if($copy.sha256 -cne $Source.sha256){throw 'BF_BLOCKED: native source snapshot identity mismatch.'}
    return $copy
}

function Get-BFNativeInventoryHash { param($Inventory) return Get-BFHash ([ordered]@{target=$Inventory.target;platform=$Inventory.platform;extensions=@($Inventory.extensions)}) }

function Read-BFNativeInventoryDirect {
    param([string]$Target,[string]$Executable,[Management.Automation.PSCredential]$Credential)
    $expectedDll=[IO.Path]::GetFullPath((Join-Path (Split-Path $Executable -Parent) 'comcntr.dll'))
    $registered=[IO.Path]::GetFullPath([string](Get-ItemPropertyValue -LiteralPath 'Registry::HKEY_CLASSES_ROOT\CLSID\{181E893D-73A4-4722-B61D-D604B3D67D47}\InprocServer32' -Name '(default)'))
    if($registered -ine $expectedDll -or -not(Test-Path -LiteralPath $expectedDll -PathType Leaf)){throw 'BF_BLOCKED: registered COM connector differs from the authorized platform.'}
    $net=$Credential.GetNetworkCredential()
    $connectionString=$null
    $connector=$null
    $connection=$null
    $manager=$null
    $array=$null
    $owned=[Collections.Generic.List[object]]::new()
    $phase='credential'
    try{
        if($net.UserName -match '[;"\x00-\x1f]' -or $net.Password -match '[;"\x00-\x1f]'){throw 'BF_BLOCKED: credential characters are unsupported by the FILE connection builder.'}
        $connectionString='File="'+$Target+'";Usr="'+$net.UserName+'";Pwd="'+$net.Password+'";'
        $phase='connect'
        $connector=New-Object -ComObject V83.COMConnector
        $connection=$connector.Connect($connectionString)
        $phase='extensions'
        try{$manager=$connection.GetType().InvokeMember('ConfigurationExtensions',[Reflection.BindingFlags]::GetProperty,$null,$connection,$null)}catch{$manager=$connection.GetType().InvokeMember('РасширенияКонфигурации',[Reflection.BindingFlags]::GetProperty,$null,$connection,$null)}
        $array=$manager.Получить()
        $count=if($array -is [array]){$array.Length}else{[int]$array.Количество()}
        $rows=@(for($i=0;$i -lt $count;$i++){
            $item=if($array -is [array]){$array[$i]}else{$array.Получить($i)}
            $owned.Add($item)
            $row=[ordered]@{}
            foreach($pair in @(@('name','Имя'),@('version','Версия'),@('active','Активно'),@('purpose','Назначение'),@('scope','ОбластьДействия'),@('uuid','УникальныйИдентификатор'),@('hash_sum','ХешСумма'))){
                try{
                    $value=$item.GetType().InvokeMember($pair[1],[Reflection.BindingFlags]::GetProperty,$null,$item,$null)
                    if($null -ne $value -and [Runtime.InteropServices.Marshal]::IsComObject($value)){
                        try{$converted=$connection.String($value)}finally{[void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($value)}
                        $value=$converted
                    }
                    $row[$pair[0]]=$value
                }catch{throw ('BF_BLOCKED: inventory property '+$pair[0]+' is unobserved.')}
            }
            [ordered]@{properties=$row}
        })
        $rows=@($rows | Sort-Object {$_.properties.name})
        return [ordered]@{schema_version=1;kind='bsl-flow.native-inventory';observed_at_utc=[DateTime]::UtcNow.ToString('o');target=[IO.Path]::GetFullPath($Target).TrimEnd('\');platform=[ordered]@{executable=$Executable;com_connector=$registered};extensions=$rows;limitations=@('Base configuration version is not included because no verified API is used for it.')}
    }catch{
        if($_.Exception.Message -match '^BF_(INVALID|BLOCKED|CONFLICT|FAIL):'){throw}
        $safe=[InvalidOperationException]::new(('BF_BLOCKED: native inventory failed during '+$phase+'.'))
        $safe.Data['error_type']=$_.Exception.GetType().FullName
        $safe.Data['hresult']=$_.Exception.HResult
        throw $safe
    }finally{
        foreach($object in $owned){if([Runtime.InteropServices.Marshal]::IsComObject($object)){[void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($object)}}
        foreach($object in @($array,$manager,$connection,$connector)){if($null -ne $object -and [Runtime.InteropServices.Marshal]::IsComObject($object)){[void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($object)}}
        $connectionString=$null
        $net=$null
    }
}

function Test-BFNativeJUnit {
    param([string]$Path,[string[]]$Expected,[datetime]$Started,[datetime]$Finished)
    if(-not(Test-Path -LiteralPath $Path -PathType Leaf) -or (Get-Item $Path).Length -eq 0){throw 'BF_BLOCKED: original native JUnit report missing or empty.'}
    $info=Get-Item $Path;if($info.LastWriteTimeUtc -lt $Started.ToUniversalTime() -or $info.LastWriteTimeUtc -gt $Finished.ToUniversalTime().AddSeconds(2)){throw 'BF_BLOCKED: native JUnit is not fresh for this attempt.'}
    $s=[Xml.XmlReaderSettings]::new();$s.DtdProcessing=[Xml.DtdProcessing]::Prohibit;$s.XmlResolver=$null;$r=[Xml.XmlReader]::Create($Path,$s)
    try{$xml=[Xml.XmlDocument]::new();$xml.XmlResolver=$null;$xml.Load($r)}finally{$r.Dispose()}
    if($xml.DocumentElement.Name -notin @('testsuite','testsuites')){throw 'BF_BLOCKED: unsupported native JUnit root.'}
    $cases=@($xml.SelectNodes('//testcase'));$ids=@($cases|ForEach-Object{$_.GetAttribute('classname')+'.'+$_.GetAttribute('name')})
    if($cases.Count -eq 0 -or @($ids|Select-Object -Unique).Count -ne $ids.Count -or (@($ids|Sort-Object)-join "`n") -cne (@($Expected|Sort-Object)-join "`n")){throw 'BF_BLOCKED: native JUnit selection differs from exact expected tests.'}
    if(@($xml.SelectNodes('//skipped')).Count){throw 'BF_BLOCKED: required native tests were skipped.'}
    $failed=@($xml.SelectNodes('//failure|//error')).Count -gt 0
    foreach($suite in @($xml.SelectNodes('//testsuite|//testsuites'))){foreach($name in @('tests','failures','errors','skipped','disabled')){if(!$suite.HasAttribute($name)){continue};$v=$suite.GetAttribute($name);if($v -cnotmatch '^(0|[1-9][0-9]*)$'){throw 'BF_BLOCKED: invalid native JUnit aggregate.'};$actual=switch($name){'tests'{@($suite.SelectNodes('.//testcase')).Count}'failures'{@($suite.SelectNodes('.//testcase[failure]')).Count}'errors'{@($suite.SelectNodes('.//testcase[error]')).Count}default{0}};if([int]$v -ne $actual){throw 'BF_BLOCKED: inconsistent native JUnit aggregate.'}}}
    if($failed){throw 'BF_FAIL: required native integration tests failed.'}
    return [ordered]@{tests=$ids;sha256=Get-BFFileHash $Path;outcome='PASS'}
}

function Assert-BFNativeInventoryTransition {
    param($Before,$After,$Source)
    $normalizedBefore=@($Before.extensions|ForEach-Object{ConvertTo-BFNativeInventoryRow $_})
    $normalizedAfter=@($After.extensions|ForEach-Object{ConvertTo-BFNativeInventoryRow $_})
    $otherBefore=@($normalizedBefore|Where-Object{$_.name -ine $Source.extension})
    $otherAfter=@($normalizedAfter|Where-Object{$_.name -ine $Source.extension})
    if((Get-BFHash $otherBefore) -cne (Get-BFHash $otherAfter)){throw 'BF_BLOCKED: a nonselected extension changed during native verification.'}
    $selectedBefore=@($normalizedBefore|Where-Object{$_.name -ieq $Source.extension})
    $selected=@($normalizedAfter|Where-Object{$_.name -ieq $Source.extension})
    if($selected.Count -ne 1 -or [string]$selected[0].version -cne $Source.version -or $selected[0].active -ne $true){throw 'BF_BLOCKED: installed extension version or active state differs from the authorized source XML.'}
    if($selectedBefore.Count -gt 1 -or ($selectedBefore.Count -eq 1 -and $selectedBefore[0].uuid -ine $selected[0].uuid)){throw 'BF_BLOCKED: installed extension instance UUID changed unexpectedly.'}
}

function ConvertTo-BFNativeInventoryRow {
    param($Item)
    $row=[ordered]@{}
    foreach($name in @('name','version','active','purpose','scope','uuid','hash_sum')){
        $value=Get-BFValue $Item.properties $name
        if($null -ne $value -and $null -ne (Get-BFValue $value 'status') ){
            if((Get-BFValue $value 'status') -cne 'observed'){throw ('BF_BLOCKED: inventory property '+$name+' is unobserved.')}
            $value=Get-BFValue $value 'value'
        }
        if($null -eq $value){throw ('BF_BLOCKED: inventory property '+$name+' is missing.')}
        $row[$name]=$value
    }
    return $row
}

function New-BFNativeArguments { param($Step,[Management.Automation.PSCredential]$Credential) $net=$Credential.GetNetworkCredential();try{if([string]::IsNullOrWhiteSpace($net.UserName)-or $net.UserName -match '["\x00-\x1f]' -or $net.Password -match '["\x00-\x1f]'){throw 'BF_BLOCKED: unsupported credential characters.'};return [string[]]@($Step.argv|ForEach-Object{if($_ -ceq '<username>'){$net.UserName}elseif($_ -ceq '<password>'){$net.Password}else{$_}})}finally{$net=$null}}

function Invoke-BFNativeProcess {
    param([string]$Executable,$Step,[Management.Automation.PSCredential]$Credential,[string]$Directory,[int]$TimeoutSeconds,[scriptblock]$Cancelled,[string]$RequestHash)
    if($null -ne $Cancelled -and (& $Cancelled)){throw 'BF_BLOCKED: cancelled before native dispatch.'}
    $deadline=[DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $runDeadline=Get-Variable BFRunDeadlineUtc -ValueOnly -ErrorAction SilentlyContinue
    if($null -ne $runDeadline -and $runDeadline -lt $deadline){$deadline=$runDeadline}
    if($deadline -le [DateTime]::UtcNow){throw 'BF_BLOCKED: task deadline reached before native dispatch.'}
    $stepDir=Join-Path $Directory ('steps/'+$Step.name);[void][IO.Directory]::CreateDirectory($stepDir)
    Write-BFJson (Join-Path $stepDir 'prepared.json') ([ordered]@{request_sha256=$RequestHash;step=$Step;prepared_at_utc=[DateTime]::UtcNow.ToString('o')})
    $args=New-BFNativeArguments $Step $Credential;$psi=[Diagnostics.ProcessStartInfo]::new();$psi.FileName=$Executable;$psi.UseShellExecute=$false;$psi.CreateNoWindow=$true;foreach($arg in $args){[void]$psi.ArgumentList.Add($arg)};$args=$null
    $p=[Diagnostics.Process]::new();$p.StartInfo=$psi
    try{
        if(($null -ne $Cancelled -and (& $Cancelled)) -or $deadline -le [DateTime]::UtcNow){
            Write-BFJson (Join-Path $stepDir 'not-started.json') @{request_sha256=$RequestHash;reason='cancelled_or_deadline_before_start'}
            throw 'BF_BLOCKED: cancellation or deadline before native start.'
        }
        if(!$p.Start()){throw 'BF_BLOCKED: native process did not start.'}
        $identity=[ordered]@{pid=$p.Id;start_time_utc=$p.StartTime.ToUniversalTime().ToString('o');request_sha256=$RequestHash;step=$Step.name;non_interruptible=$true}
        Write-BFJson (Join-Path $stepDir 'process.json') $identity
        Write-BFJson (Join-Path $stepDir 'started.json') $identity
        while(!$p.HasExited -and [DateTime]::UtcNow -lt $deadline){
            Start-Sleep -Milliseconds 200
            if($null -ne $Cancelled -and (& $Cancelled)){throw 'BF_BLOCKED: cancellation observed after native dispatch; process was not killed.'}
        }
        if(!$p.HasExited){throw 'BF_BLOCKED: native process timed out and was not killed.'}
        $terminal=[ordered]@{request_sha256=$RequestHash;process=$identity;exit_code=$p.ExitCode;finished_at_utc=[DateTime]::UtcNow.ToString('o');log=$Step.log;log_sha256=if(Test-Path -LiteralPath $Step.log -PathType Leaf){Get-BFFileHash $Step.log}else{$null}};Write-BFJson (Join-Path $stepDir 'terminal.json') $terminal
        if($terminal.exit_code -ne 0 -or $null -eq $terminal.log_sha256){throw 'BF_BLOCKED: native step did not complete with a durable successful log.'};return $terminal
    }finally{$p.Dispose()}
}

function Read-BFNativeInventory {
    param([string]$Target,[string]$Executable,[Management.Automation.PSCredential]$Credential,[string]$Directory,[scriptblock]$Cancelled)
    if(-not $Directory){throw 'BF_BLOCKED: inventory requires an owned evidence directory.'}
    $helper=Join-Path $PSScriptRoot 'Read-NativeInventory.ps1'
    $arguments=@('-NoProfile','-NonInteractive','-File',$helper,'-Target',$Target,'-Executable',$Executable)
    $net=$Credential.GetNetworkCredential()
    try{
        $inputText=Get-BFCanonicalJson @{username=$net.UserName;password=$net.Password}
        $process=Invoke-BFProcess (Join-Path $PSHOME 'pwsh.exe') $arguments $PSScriptRoot ($inputText+"`n") $Directory 120 $Cancelled
    }finally{$inputText=$null;$net=$null}
    if($process.stop_reason -or $process.exit_code -ne 0){throw 'BF_BLOCKED: bounded read-only native inventory did not complete.'}
    $inventory=Read-BFJson $process.stdout
    if($inventory.kind -ne 'bsl-flow.native-inventory' -or $inventory.target -ine $Target){throw 'BF_BLOCKED: native inventory identity mismatch.'}
    return $inventory
}

function Get-BFNativeCredential { if($null -eq (Get-Variable -Scope Script -Name BFNativeCredential -ErrorAction SilentlyContinue) -or $script:BFNativeCredential -isnot [Management.Automation.PSCredential]){throw 'BF_BLOCKED: native credential must be supplied through private controller input for this process.'};return $script:BFNativeCredential }
function Get-BFNativeJournalRoot { param([string]$TargetKey) Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)) ('BSLFlow/runtime/'+$TargetKey) }

function Invoke-BFNativeVerification {
    param($State,$Criterion,[string]$Directory,[scriptblock]$Cancelled)
    Assert-BFNativeCriterion $Criterion;$attemptId=$State.active_attempt;if($attemptId -isnot [string]){throw 'BF_CONFLICT: registered active attempt is required for native verification.'}
    $target=[IO.Path]::GetFullPath($Criterion.target).TrimEnd('\');$key=Get-BFRuntimeTargetKey $target;$journal=Get-BFNativeJournalRoot $key;$lock=$null;$pendingWritten=$false
    try{
        $lock=Enter-BFLock $journal
        $pendingPath=Join-Path $journal 'pending.json';if(Test-Path -LiteralPath $pendingPath){throw 'BF_BLOCKED: target has an unresolved native attempt; automatic replay is forbidden.'}
        if(-not(Test-Path -LiteralPath (Join-Path $target '1Cv8.1CD') -PathType Leaf)){throw 'BF_BLOCKED: authorized FILE target marker is missing.'}
        if((Get-BFFileHash $Criterion.executable) -cne $Criterion.native_1c.executable_sha256){throw 'BF_BLOCKED: authorized 1cv8.exe identity changed.'}
        $physical=Get-BFRuntimeTargetIdentity $target;if($physical -ine $target){throw 'BF_BLOCKED: native target does not match its physical identity.'}
        $fullSource=Get-BFSourceManifest $State
        $source=Get-BFNativeSource (Join-Path $State.worker_path $Criterion.native_1c.source_root);if($source.extension -cne $Criterion.native_1c.extension){throw 'BF_BLOCKED: source extension name differs from criterion.'}
        $snapshot=Copy-BFNativeSnapshot $source (Join-Path $Directory 'source-snapshot');$credential=Get-BFNativeCredential;$before=Read-BFNativeInventory $target $Criterion.executable $credential (Join-Path $Directory 'inventory-process-before') $Cancelled
        Write-BFJson (Join-Path $Directory 'inventory-before.json') $before
        $loadedProof=Get-BFNativeLoadedProof $State $Criterion $source $before
        if($null -ne $loadedProof){Write-BFJson (Join-Path $Directory 'loaded-proof.json') $loadedProof}
        $request=[ordered]@{schema_version=1;kind='bsl-flow.native-1c-request';task_id=$State.task_id;attempt_id=$attemptId;criterion_id=$Criterion.id;target=$target;target_key=$key;source=[ordered]@{extension=$source.extension;version=$source.version;uuid=$source.uuid;sha256=$source.sha256};platform=[ordered]@{version=$Criterion.native_1c.platform_version;executable_sha256=$Criterion.native_1c.executable_sha256};operations=@($Criterion.native_1c.authorized_operations);authorization_reference=$Criterion.native_1c.authorization_reference;expected_tests=@($Criterion.expected_tests)};$requestHash=Get-BFHash $request;Write-BFJson (Join-Path $Directory 'runtime-request.json') $request
        $pending=[ordered]@{schema_version=1;state='dispatched_or_unknown';task_id=$State.task_id;attempt_id=$attemptId;criterion_id=$Criterion.id;target=$target;source_root=$Criterion.native_1c.source_root;full_source_sha256=$fullSource.sha256;extension_source_sha256=$source.sha256;request_sha256=$requestHash;created_at_utc=[DateTime]::UtcNow.ToString('o')};Write-BFJson $pendingPath $pending;$pendingWritten=$true
        $base=@('DESIGNER','/DisableStartupDialogs','/DisableStartupMessages','/F',$target,'/N','<username>','/P','<password>');$steps=@([ordered]@{name='load';log=Join-Path $Directory 'load.out.log';argv=@($base+@('/Out',(Join-Path $Directory 'load.out.log'),'-NoTruncate','/LoadConfigFromFiles',(Join-Path $Directory 'source-snapshot'),'-Extension',$source.extension))},[ordered]@{name='update';log=Join-Path $Directory 'update.out.log';argv=@($base+@('/Out',(Join-Path $Directory 'update.out.log'),'-NoTruncate','/UpdateDBCfg','-Extension',$source.extension))})
        $timeout=[int](Get-BFValue $State.request 'timeout_seconds' 1800);if($null -ne $loadedProof){$steps=@()};foreach($step in $steps){[void](Invoke-BFNativeProcess $Criterion.executable $step $credential $Directory $timeout $Cancelled $requestHash);if((Get-BFNativeSource $source.root).sha256 -cne $source.sha256){throw 'BF_BLOCKED: worker source changed during native execution.'}}
        if($null -eq $loadedProof){
            $loaded=Read-BFNativeInventory $target $Criterion.executable $credential (Join-Path $Directory 'inventory-process-loaded') $Cancelled
            Write-BFJson (Join-Path $Directory 'inventory-loaded.json') $loaded
            Assert-BFNativeInventoryTransition $before $loaded $source
        }
        $report=Join-Path $Directory 'original.junit.xml';$config=[ordered]@{filter=[ordered]@{modules=@($Criterion.native_1c.module)};reportFormat='jUnit';reportPath=$report;closeAfterTests=$true;showReport=$false;logging=[ordered]@{file=(Join-Path $Directory 'runner.log');console=$false;level='info'}};Write-BFJson (Join-Path $Directory 'test-config.json') $config
        $test=[ordered]@{name='test';log=Join-Path $Directory 'enterprise.out.log';argv=@('ENTERPRISE','/DisableStartupDialogs','/F',$target,'/N','<username>','/P','<password>','/C',('RunUnitTests='+(Join-Path $Directory 'test-config.json').Replace('\','/')),'/Out',(Join-Path $Directory 'enterprise.out.log'))};$terminal=Invoke-BFNativeProcess $Criterion.executable $test $credential $Directory $timeout $Cancelled $requestHash
        if((Get-BFNativeSource $source.root).sha256 -cne $source.sha256){throw 'BF_BLOCKED: worker source changed during native execution.'};$parsed=Test-BFNativeJUnit $report @($Criterion.expected_tests) ([datetime]::Parse($terminal.process.start_time_utc)) ([datetime]::Parse($terminal.finished_at_utc));$after=Read-BFNativeInventory $target $Criterion.executable $credential (Join-Path $Directory 'inventory-process-after') $Cancelled;Write-BFJson (Join-Path $Directory 'inventory-after.json') $after;Assert-BFNativeInventoryTransition $before $after $source;if($null -ne $loadedProof -and (Get-BFNativeInventoryHash $before) -cne (Get-BFNativeInventoryHash $after)){throw 'BF_BLOCKED: test-only run changed installed extension inventory.'}
        $receipt=[ordered]@{schema_version=1;kind='bsl-flow.native-1c-success';task_id=$State.task_id;attempt_id=$attemptId;request_sha256=$requestHash;source_sha256=$source.sha256;inventory_before_sha256=Get-BFNativeInventoryHash $before;inventory_after_sha256=Get-BFNativeInventoryHash $after;junit_sha256=$parsed.sha256;tests=$parsed.tests;completed_at_utc=[DateTime]::UtcNow.ToString('o')}
        Write-BFJson (Join-Path $Directory 'runtime-success.json') $receipt
        $history=Join-Path $journal 'history';[void][IO.Directory]::CreateDirectory($history);Write-BFJson (Join-Path $history ($attemptId+'.success.json')) $receipt;Remove-Item -LiteralPath $pendingPath -Force
        return [ordered]@{criterion_id=$Criterion.id;kind=$Criterion.kind;tests=$parsed.tests;sha256=$parsed.sha256;outcome='PASS';runtime=[ordered]@{target_key=$key;target=$target;source_sha256=$source.sha256;extension=$source.extension;version=$source.version;uuid=$source.uuid;inventory_before_sha256=$receipt.inventory_before_sha256;inventory_after_sha256=$receipt.inventory_after_sha256;request_sha256=$requestHash}}
    }catch{
        $safeMessage=if($_.Exception.Message -match '^BF_(INVALID|BLOCKED|CONFLICT|FAIL):'){$_.Exception.Message}else{'BF_BLOCKED: native adapter failed; inspect sanitized attempt evidence.'}
        $code=($safeMessage -split ':')[0]
        try{
            $failure=[ordered]@{code=$code;error_type=$_.Exception.GetType().FullName;failed_at_utc=[DateTime]::UtcNow.ToString('o')}
            Write-BFJson (Join-Path $Directory 'runtime-failure.json') $failure
            if(-not $pendingWritten){Write-BFJson (Join-Path $Directory 'runtime-no-dispatch.json') ([ordered]@{schema_version=1;kind='bsl-flow.native-1c-no-dispatch';task_id=$State.task_id;attempt_id=$attemptId;criterion_id=$Criterion.id;state='preflight_failed_before_pending';failure=$failure})}
        }catch{}
        $errorToThrow=[InvalidOperationException]::new($safeMessage)
        if(-not $pendingWritten -and (Test-Path -LiteralPath (Join-Path $Directory 'runtime-no-dispatch.json'))){$errorToThrow.Data['BF_NativeNotDispatched']=$true}
        throw $errorToThrow
    }finally{if($null -ne $lock){$lock.Dispose()}}
}

function Test-BFNativeProcessDead { param($Identity) if($null -eq $Identity){return $true};$p=Get-Process -Id $Identity.pid -ErrorAction SilentlyContinue;return ($null -eq $p -or $p.StartTime.ToUniversalTime().ToString('o') -cne $Identity.start_time_utc) }

function Get-BFNativeSavedObservation {
    param($State,$Criterion,[string]$Directory,[string]$AttemptId)
    $receipt=Read-BFJson (Join-Path $Directory 'runtime-success.json')
    $request=Read-BFJson (Join-Path $Directory 'runtime-request.json')
    $requestHash=Get-BFHash $request
    if($receipt.kind -ne 'bsl-flow.native-1c-success' -or $receipt.task_id -ne $State.task_id -or $receipt.attempt_id -ne $AttemptId -or $receipt.request_sha256 -ne $requestHash){throw 'BF_BLOCKED: saved native success identity mismatch.'}
    if($request.task_id -ne $State.task_id -or $request.attempt_id -ne $AttemptId -or $request.criterion_id -ne $Criterion.id -or $request.target -ine $Criterion.target -or (Get-BFHash $request.expected_tests) -ne (Get-BFHash $Criterion.expected_tests)){throw 'BF_BLOCKED: saved native request differs from current criterion.'}
    $source=Get-BFNativeSource (Join-Path $State.worker_path $Criterion.native_1c.source_root)
    $snapshot=Get-BFNativeSource (Join-Path $Directory 'source-snapshot')
    if($source.sha256 -ne $request.source.sha256 -or $snapshot.sha256 -ne $source.sha256 -or $receipt.source_sha256 -ne $source.sha256){throw 'BF_BLOCKED: saved native source snapshot is stale.'}
    $before=Read-BFJson (Join-Path $Directory 'inventory-before.json')
    $loadedProof=Get-BFNativeLoadedProof $State $Criterion $source $before
    $steps=@('load','update','test')
    if($null -ne $loadedProof){
        if((Get-BFHash (Read-BFJson (Join-Path $Directory 'loaded-proof.json'))) -cne (Get-BFHash $loadedProof)){throw 'BF_BLOCKED: saved loaded source proof changed.'}
        $steps=@('test')
    }
    foreach($name in $steps){
        $terminal=Read-BFJson (Join-Path $Directory ('steps/'+$name+'/terminal.json'))
        if($terminal.request_sha256 -ne $requestHash -or $terminal.exit_code -ne 0 -or (Get-BFFileHash (Assert-BFSafePath $terminal.log)) -ne $terminal.log_sha256){throw 'BF_BLOCKED: incomplete saved native terminal evidence.'}
    }
    $parsed=Test-BFNativeJUnit (Join-Path $Directory 'original.junit.xml') @($Criterion.expected_tests) ([datetime]::Parse($terminal.process.start_time_utc)) ([datetime]::Parse($terminal.finished_at_utc))
    $before=Read-BFJson (Join-Path $Directory 'inventory-before.json')
    $after=Read-BFJson (Join-Path $Directory 'inventory-after.json')
    if($parsed.sha256 -ne $receipt.junit_sha256 -or (Get-BFNativeInventoryHash $before) -ne $receipt.inventory_before_sha256 -or (Get-BFNativeInventoryHash $after) -ne $receipt.inventory_after_sha256){throw 'BF_BLOCKED: saved native reports changed.'}
    Assert-BFNativeInventoryTransition $before $after $source
    if($null -ne $loadedProof -and (Get-BFNativeInventoryHash $before) -cne (Get-BFNativeInventoryHash $after)){throw 'BF_BLOCKED: saved test-only inventory changed.'}
    return [ordered]@{criterion_id=$Criterion.id;kind=$Criterion.kind;tests=$parsed.tests;sha256=$parsed.sha256;outcome='PASS';runtime=[ordered]@{target_key=$request.target_key;target=$request.target;source_sha256=$source.sha256;extension=$source.extension;version=$source.version;uuid=$source.uuid;inventory_before_sha256=$receipt.inventory_before_sha256;inventory_after_sha256=$receipt.inventory_after_sha256;request_sha256=$requestHash}}
}

function Complete-BFNativeSavedSuccess {
    param($State,[string]$Directory,[string]$AttemptId)
    $request=Read-BFJson (Join-Path $Directory 'runtime-request.json')
    $receipt=Read-BFJson (Join-Path $Directory 'runtime-success.json')
    $journal=Get-BFNativeJournalRoot (Get-BFRuntimeTargetKey $request.target)
    $lock=Enter-BFLock $journal
    try{
        $pendingPath=Join-Path $journal 'pending.json'
        if(-not(Test-Path -LiteralPath $pendingPath)){return}
        $pending=Read-BFJson $pendingPath
        if($pending.task_id -ne $State.task_id -or $pending.attempt_id -ne $AttemptId){return}
        if($pending.request_sha256 -ne $receipt.request_sha256 -or $receipt.request_sha256 -ne (Get-BFHash $request)){throw 'BF_BLOCKED: saved native success does not resolve the target latch.'}
        $history=Join-Path $journal 'history';[void][IO.Directory]::CreateDirectory($history)
        $path=Join-Path $history ($AttemptId+'.success.json')
        if(Test-Path -LiteralPath $path){if((Get-BFHash (Read-BFJson $path)) -ne (Get-BFHash $receipt)){throw 'BF_CONFLICT: conflicting native success history.'}}
        else{Write-BFJson $path $receipt}
        Remove-Item -LiteralPath $pendingPath -Force
    }finally{$lock.Dispose()}
}

function Complete-BFRecordedNativeSuccess {
    param($State,[string]$AttemptId)
    $entry=@($State.evidence | Where-Object { $_.attempt_id -eq $AttemptId -and $_.stage -eq 'verify' -and $_.outcome -eq 'PASS' })
    if($entry.Count -ne 1){return}
    $attempt=Join-Path (Get-BFTaskDirectory $State.project_path $State.task_id) ('attempts/'+$AttemptId)
    $rawRoot=Join-Path $attempt 'raw'
    $successes=@(Get-ChildItem -LiteralPath $rawRoot -Filter runtime-success.json -File -Recurse | Where-Object { $_.Directory.Parent.FullName -eq $rawRoot })
    if($successes.Count -eq 0){return}
    $result=Read-BFJson (Join-Path $attempt 'result.json')
    if((Get-BFHash $result) -ne $entry[0].result_sha256){throw 'BF_BLOCKED: recorded native result changed before completion.'}
    foreach($raw in $result.raw_hashes){
        $path=Assert-BFSafePath $raw.path
        if(-not $path.StartsWith($attempt+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase) -or (Get-BFFileHash $path) -ne $raw.sha256){throw 'BF_BLOCKED: recorded native evidence changed before completion.'}
    }
    foreach($success in $successes){Complete-BFNativeSavedSuccess $State $success.DirectoryName $AttemptId}
}

function Resolve-BFNativeRecovery {
    param($State,$Resolution,[string]$AttemptDir)
    Assert-BFFields $Resolution @('attempt_id','scope','target','source_sha256','inventory_sha256','observation','retry_authorized') @() 'resolution'
    $unresolvedAttempt=if($null -ne $State.active_attempt){$State.active_attempt}elseif($null -ne $State.unresolved_effect){$State.unresolved_effect.attempt_id}else{$null}
    if($Resolution.scope -cne 'native_1c' -or $Resolution.attempt_id -cne $unresolvedAttempt -or $Resolution.retry_authorized -ne $true){throw 'BF_BLOCKED: exact native recovery with explicit retry authorization is required.'};Assert-BFText $Resolution.observation 'resolution.observation'
    $target=[IO.Path]::GetFullPath($Resolution.target).TrimEnd('\');$key=Get-BFRuntimeTargetKey $target;$journal=Get-BFNativeJournalRoot $key;$lock=Enter-BFLock $journal
    try{$identity=[ordered]@{task_id=$State.task_id;attempt_id=$Resolution.attempt_id;target=$target;source_sha256=$Resolution.source_sha256;inventory_sha256=$Resolution.inventory_sha256;observation=$Resolution.observation;retry_authorized=$true};$receiptId=Get-BFHash $identity;$history=Join-Path $journal 'recovery';[void][IO.Directory]::CreateDirectory($history);$receiptPath=Join-Path $history ($receiptId+'.json')
        $pendingPath=Join-Path $journal 'pending.json';if(-not(Test-Path -LiteralPath $pendingPath)){throw 'BF_BLOCKED: native pending record is missing and no matching recovery receipt exists.'};$pending=Read-BFJson $pendingPath
        if($pending.task_id -cne $State.task_id -or $pending.attempt_id -cne $Resolution.attempt_id -or $pending.target -ine $target -or $pending.full_source_sha256 -cne $Resolution.source_sha256){throw 'BF_CONFLICT: native recovery identity mismatch.'}
        if(Test-Path -LiteralPath $receiptPath){
            $receipt=Read-BFJson $receiptPath
            if((Get-BFHash $receipt.identity) -cne $receiptId){throw 'BF_CONFLICT: retained recovery identity changed.'}
            if((Get-BFNativeInventoryHash (Read-BFJson $receipt.inventory_path)) -cne $receipt.inventory_sha256){throw 'BF_BLOCKED: retained control-read inventory changed.'}
            $marked=Get-BFValue $pending 'reconciled_identity_sha256'
            if($null -eq $marked -and (Get-BFFileHash $pendingPath) -cne $receipt.pending_sha256){throw 'BF_CONFLICT: unreconciled pending record changed after control read.'}
            if($null -ne $marked -and $marked -cne $receiptId){throw 'BF_CONFLICT: pending record has another recovery.'}
            $pending|Add-Member -NotePropertyName reconciled_receipt_sha256 -NotePropertyValue (Get-BFFileHash $receiptPath) -Force
            $pending|Add-Member -NotePropertyName reconciled_identity_sha256 -NotePropertyValue $receiptId -Force
            Write-BFJson $pendingPath $pending -Replace
            return $receipt
        }
        $start=Read-BFJson (Join-Path $AttemptDir 'start.json')
        if(-not(Test-BFNativeProcessDead $start.controller_process)){throw 'BF_BLOCKED: original native controller is still running.'}
        foreach($prepared in Get-ChildItem -LiteralPath $AttemptDir -Filter 'prepared.json' -File -Recurse){
            $processPath=Join-Path $prepared.DirectoryName 'process.json'
            $notStarted=Join-Path $prepared.DirectoryName 'not-started.json'
            if(-not(Test-Path -LiteralPath $processPath -PathType Leaf) -and -not(Test-Path -LiteralPath $notStarted -PathType Leaf)){throw 'BF_BLOCKED: native process identity gap requires independent operator investigation.'}
            if(Test-Path -LiteralPath $notStarted){
                if((Read-BFJson $notStarted).request_sha256 -cne $pending.request_sha256){throw 'BF_BLOCKED: no-start receipt has a different native request.'}
            }
        }
        foreach($file in Get-ChildItem -LiteralPath $AttemptDir -Filter 'process.json' -File -Recurse){if(-not(Test-BFNativeProcessDead (Read-BFJson $file.FullName))){throw 'BF_BLOCKED: native child process is still running.'}}
        $criterion=@($State.request.criteria|Where-Object{$_.id -ceq $pending.criterion_id})
        if($criterion.Count -ne 1 -or $criterion[0].target -ine $target){throw 'BF_BLOCKED: pending native criterion is unavailable.'}
        $source=Get-BFSourceManifest $State
        if($source.sha256 -cne $Resolution.source_sha256){throw 'BF_CONFLICT: recovery source control read is stale.'}
        $credential=Get-BFNativeCredential
        $inventoryDirectory=Join-Path $history ($receiptId+'-read-'+[guid]::NewGuid().ToString('N'))
        $inventory=Read-BFNativeInventory $target $criterion[0].executable $credential $inventoryDirectory { $false }
        $actualHash=Get-BFNativeInventoryHash $inventory
        if($actualHash -cne $Resolution.inventory_sha256){throw 'BF_CONFLICT: recovery inventory control read differs from the trusted resolution.'}
        $control=Join-Path $history ($receiptId+'.inventory.json');Write-BFJson $control $inventory;$receipt=[ordered]@{schema_version=1;kind='bsl-flow.native-1c-recovery';identity=$identity;inventory_path=$control;inventory_sha256=$actualHash;pending_sha256=Get-BFFileHash $pendingPath;resolved_at_utc=[DateTime]::UtcNow.ToString('o');verdict='RECONCILED_NO_PASS'};Write-BFJson $receiptPath $receipt;$pending|Add-Member -NotePropertyName reconciled_receipt_sha256 -NotePropertyValue (Get-BFFileHash $receiptPath) -Force;$pending|Add-Member -NotePropertyName reconciled_identity_sha256 -NotePropertyValue $receiptId -Force;Write-BFJson $pendingPath $pending -Replace;return $receipt
    }finally{$lock.Dispose()}
}

function Complete-BFNativeRecovery {
    param($State,$Resolution)
    $target=[IO.Path]::GetFullPath($Resolution.target).TrimEnd('\');$key=Get-BFRuntimeTargetKey $target;$journal=Get-BFNativeJournalRoot $key;$lock=Enter-BFLock $journal
    try{$identity=[ordered]@{task_id=$State.task_id;attempt_id=$Resolution.attempt_id;target=$target;source_sha256=$Resolution.source_sha256;inventory_sha256=$Resolution.inventory_sha256;observation=$Resolution.observation;retry_authorized=$true};$receiptId=Get-BFHash $identity;$receiptPath=Join-Path $journal ('recovery/'+$receiptId+'.json');if(-not(Test-Path -LiteralPath $receiptPath -PathType Leaf)){throw 'BF_BLOCKED: durable native recovery receipt is missing.'};$receipt=Read-BFJson $receiptPath;if((Get-BFHash $receipt.identity) -cne (Get-BFHash $identity)){throw 'BF_CONFLICT: native recovery receipt identity mismatch.'};$pendingPath=Join-Path $journal 'pending.json';if(-not(Test-Path -LiteralPath $pendingPath)){return $receipt};$pending=Read-BFJson $pendingPath;if($pending.task_id -cne $State.task_id -or $pending.attempt_id -cne $Resolution.attempt_id -or $pending.reconciled_identity_sha256 -cne $receiptId -or $pending.reconciled_receipt_sha256 -cne (Get-BFFileHash $receiptPath)){throw 'BF_CONFLICT: native pending latch differs from the committed recovery.'};Remove-Item -LiteralPath $pendingPath -Force;return $receipt}finally{$lock.Dispose()}
}
