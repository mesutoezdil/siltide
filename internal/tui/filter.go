package tui

import "github.com/moezdil/siltide/internal/query"

// The filter language lives in internal/query, because it is a query over
// devices and processes rather than anything to do with drawing them: the
// interface, the API and the MCP server all take the same string.
type Filter = query.Filter

// ParseFilter reads "python ns:ml dev:3 util>80 !vendor:amd".
func ParseFilter(s string) Filter { return query.ParseFilter(s) }
