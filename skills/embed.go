// Package skills embeds the agent skill so `dbmeta-cli skill install` works from a single binary.
package skills

import "embed"

//go:embed dbmeta/SKILL.md
var FS embed.FS

// Name is both the skill's directory name and its frontmatter name.
const Name = "dbmeta"
