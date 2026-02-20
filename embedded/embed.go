// Package embedded provides the embedded prompt and skill files.
package embedded

import "embed"

//go:embed prompt.md prd-skill.md ralph-skill.md
var FS embed.FS
