$script:BSLFlowSpecAnchorSections = @(
    [pscustomobject]@{ prefix = 'REQ'; pattern = '(?:Требуемое поведение|Required behavior)' },
    [pscustomobject]@{ prefix = 'AC'; pattern = '(?:Критерии при[её]мки|Acceptance criteria)' },
    [pscustomobject]@{ prefix = 'NG'; pattern = '(?:Не делать|Non-goals)' }
)

function Get-BSLFlowSpecAnchors {
    # Stable positional anchors for the numbered/bulleted items of the required
    # behavior, acceptance criteria and non-goals sections. The item rule is the
    # requirement-manifest rule, so REQ-N is the same item as manifest REQ-00N.
    # Ids are REQ-N / AC-N / NG-N (1-based, per section).
    param([Parameter(Mandatory)][AllowEmptyString()][string]$SpecText)
    $anchors = [System.Collections.Generic.List[object]]::new()
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        foreach ($section in $script:BSLFlowSpecAnchorSections) {
            $match = [regex]::Match($SpecText, ('(?ims)^##\s+(?<title>' + $section.pattern + ')\s*$\s*(?<body>.*?)(?=^##\s|\z)'))
            if (-not $match.Success) { continue }
            $title = $match.Groups['title'].Value.Trim()
            $index = 0
            foreach ($line in ($match.Groups['body'].Value -split "`r?`n")) {
                $item = [regex]::Match($line, '^\s*(?:\d+\.\s+|[-*]\s+)(?<item>\S.*\S|\S)\s*$')
                if (-not $item.Success) { continue }
                $text = $item.Groups['item'].Value.Trim()
                if (-not $text -or $text -match '^\s*<!--') { continue }
                $index++
                $anchors.Add([pscustomobject][ordered]@{
                        id = ('{0}-{1}' -f $section.prefix, $index)
                        section = $title
                        item = $index
                        text_sha256 = ([BitConverter]::ToString($sha.ComputeHash($utf8.GetBytes($text)))).Replace('-', '').ToLowerInvariant()
                        text = $text
                    })
            }
        }
    }
    finally { $sha.Dispose() }
    return @($anchors)
}

