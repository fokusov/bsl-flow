# Registered package suites

`Test-BSLFlowPackage.ps1` runs every `*.suite` file here in name order. Each file holds one
package-relative script path, for example `scripts\Test-SkillLinks.ps1`. The script must accept
`-PackageRoot` and throw on failure.
