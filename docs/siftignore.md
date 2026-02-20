# .siftignore

Place a `.siftignore` file at the root of any collection to exclude files during indexing. Uses gitignore-style glob patterns powered by [doublestar](https://github.com/bmatcuk/doublestar).

## Example

```
# Machine-generated metadata
*.attention.json
*.summary.md

# Directories
.obsidian
archive

# Specific patterns
**/drafts/**
*.tmp
```

## How It Works

- Patterns are loaded once per collection at the start of `sift refresh`
- Matching uses [doublestar](https://github.com/bmatcuk/doublestar) glob syntax, which supports `**` for recursive directory matching
- Paths are matched relative to the collection root
- First matching pattern wins (a file matching any pattern is excluded)

## Pattern Syntax

| Pattern | Matches |
|---------|---------|
| `*.md` | All markdown files in the root |
| `**/*.json` | All JSON files at any depth |
| `.obsidian` | The `.obsidian` directory and its contents |
| `archive` | The `archive` directory and its contents |
| `**/drafts/**` | Any file under a `drafts/` directory at any depth |
| `*.{tmp,bak}` | Files with `.tmp` or `.bak` extensions |

## Tips

- One pattern per line
- Lines starting with `#` are comments
- Empty lines are ignored
- Patterns are relative to the collection root directory
- After updating `.siftignore`, run `sift refresh --full` to remove previously indexed files that now match
