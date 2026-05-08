// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package sulpher

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Algorithm represents traversal algorithm
type Algorithm string

const (
	BFS Algorithm = "BFS"
	DFS Algorithm = "DFS"
)

// Operator represents comparison operator
type Operator string

const (
	OpEq  Operator = "="
	OpNe  Operator = "!="
	OpLt  Operator = "<"
	OpGt  Operator = ">"
	OpLte Operator = "<="
	OpGte Operator = ">="
)

// NodePattern represents a node in the query pattern
type NodePattern struct {
	Variable   string                 // e.g., "u"
	Type       string                 // e.g., "User" (optional)
	Properties map[string]interface{} // inline properties like {id: 123}
}

// RelPattern represents a relationship in the query pattern
type RelPattern struct {
	Variable      string // e.g., "r" (optional)
	Type          string // e.g., "FOLLOWS" (optional)
	MinHops       int    // Minimum hops (0 = no minimum, default 1)
	MaxHops       int    // Maximum hops (0 = unlimited, use query max_depth)
	IsVariable    bool   // True if *min..max syntax was used
	Direction     RelDirection // Direction of traversal
}

// RelDirection represents relationship direction
type RelDirection int

const (
	RelOutgoing     RelDirection = iota // -[r]->
	RelIncoming                         // <-[r]-
	RelBidirectional                    // -[r]- or <-[r]->
)

// PathElement represents one step in the path pattern
type PathElement struct {
	Node         NodePattern
	Relationship *RelPattern // nil for the last node
}

// Condition represents a WHERE condition
type Condition struct {
	VarPath  string      // e.g., "u.name"
	Operator Operator    // e.g., "="
	Value    interface{} // e.g., "Alice" or 123
}

// ConditionGroup represents conditions joined by AND (groups are joined by OR)
type ConditionGroup struct {
	Conditions []Condition
}

// OrderDirection represents sort direction
type OrderDirection string

const (
	OrderAsc  OrderDirection = "ASC"
	OrderDesc OrderDirection = "DESC"
)

// OrderByItem represents an ORDER BY clause item
type OrderByItem struct {
	VarPath   string         // e.g., "u.name"
	Direction OrderDirection // ASC or DESC
}

// ReturnItem represents an item in the RETURN clause
type ReturnItem struct {
	Variable string // e.g., "u"
	Property string // e.g., "name" (empty if returning whole node)
}

// Query represents a parsed Sulpher query
type Query struct {
	Algorithm       Algorithm
	Path            []PathElement
	Conditions      []Condition      // Legacy: simple AND-joined conditions
	ConditionGroups []ConditionGroup // OR-joined groups of AND-joined conditions
	ReturnItems     []ReturnItem
	Distinct        bool          // RETURN DISTINCT
	OrderBy         []OrderByItem // ORDER BY clause
	Limit           int           // LIMIT clause (0 = no limit)
	RawQuery        string
}

// Parser parses Sulpher queries
type Parser struct{}

// NewParser creates a new Sulpher parser
func NewParser() *Parser {
	return &Parser{}
}

// Parse parses a Sulpher query string
func (p *Parser) Parse(query string) (*Query, error) {
	query = strings.TrimSpace(query)
	originalQuery := query
	upperQuery := strings.ToUpper(query)

	// Extract algorithm prefix
	algorithm := BFS
	if strings.HasPrefix(upperQuery, "BFS ") {
		algorithm = BFS
		query = strings.TrimSpace(query[4:])
		upperQuery = strings.ToUpper(query)
	} else if strings.HasPrefix(upperQuery, "DFS ") {
		algorithm = DFS
		query = strings.TrimSpace(query[4:])
		upperQuery = strings.ToUpper(query)
	}

	// Must start with MATCH
	if !strings.HasPrefix(upperQuery, "MATCH ") {
		return nil, fmt.Errorf("invalid Sulpher query format: expected MATCH")
	}
	query = strings.TrimSpace(query[6:])
	upperQuery = strings.ToUpper(query)

	// Find WHERE, RETURN positions
	whereIdx := findKeyword(upperQuery, "WHERE")
	returnIdx := findKeyword(upperQuery, "RETURN")

	if returnIdx == -1 {
		return nil, fmt.Errorf("invalid Sulpher query format: expected RETURN")
	}

	// Extract path pattern
	var pathStr string
	if whereIdx != -1 && whereIdx < returnIdx {
		pathStr = strings.TrimSpace(query[:whereIdx])
	} else {
		pathStr = strings.TrimSpace(query[:returnIdx])
	}

	// Extract WHERE clause
	var whereStr string
	if whereIdx != -1 && whereIdx < returnIdx {
		whereStr = strings.TrimSpace(query[whereIdx+6 : returnIdx])
	}

	// Extract everything after RETURN
	afterReturnStart := returnIdx + 7
	if afterReturnStart > len(query) {
		return nil, fmt.Errorf("invalid Sulpher query format: nothing after RETURN")
	}
	afterReturn := strings.TrimSpace(query[afterReturnStart:])
	if afterReturn == "" {
		return nil, fmt.Errorf("invalid Sulpher query format: empty RETURN clause")
	}
	upperAfterReturn := strings.ToUpper(afterReturn)

	// Check for DISTINCT
	distinct := false
	if strings.HasPrefix(upperAfterReturn, "DISTINCT ") {
		distinct = true
		afterReturn = strings.TrimSpace(afterReturn[9:])
		upperAfterReturn = strings.ToUpper(afterReturn)
	}

	// Find ORDER BY and LIMIT
	orderByIdx := findKeyword(upperAfterReturn, "ORDER BY")
	limitIdx := findKeyword(upperAfterReturn, "LIMIT")

	// Extract return items
	var returnStr string
	if orderByIdx != -1 {
		returnStr = strings.TrimSpace(afterReturn[:orderByIdx])
	} else if limitIdx != -1 {
		returnStr = strings.TrimSpace(afterReturn[:limitIdx])
	} else {
		returnStr = afterReturn
	}

	// Extract ORDER BY
	var orderByStr string
	if orderByIdx != -1 {
		if limitIdx != -1 && limitIdx > orderByIdx {
			orderByStr = strings.TrimSpace(afterReturn[orderByIdx+9 : limitIdx])
		} else {
			orderByStr = strings.TrimSpace(afterReturn[orderByIdx+9:])
		}
	}

	// Extract LIMIT
	var limitStr string
	if limitIdx != -1 {
		limitStr = strings.TrimSpace(afterReturn[limitIdx+6:])
	}

	// Parse path pattern
	path, err := p.parsePath(pathStr)
	if err != nil {
		return nil, fmt.Errorf("invalid path pattern: %w", err)
	}

	// Parse WHERE conditions (with OR support)
	var conditions []Condition
	var conditionGroups []ConditionGroup
	if whereStr != "" {
		conditionGroups, err = p.parseWhereWithOr(whereStr)
		if err != nil {
			return nil, fmt.Errorf("invalid WHERE clause: %w", err)
		}
		// For backward compatibility, flatten if only AND conditions
		if len(conditionGroups) == 1 {
			conditions = conditionGroups[0].Conditions
		}
	}

	// Parse RETURN items
	returnItems, err := p.parseReturn(returnStr)
	if err != nil {
		return nil, fmt.Errorf("invalid RETURN clause: %w", err)
	}

	// Parse ORDER BY
	var orderBy []OrderByItem
	if orderByStr != "" {
		orderBy, err = p.parseOrderBy(orderByStr)
		if err != nil {
			return nil, fmt.Errorf("invalid ORDER BY clause: %w", err)
		}
	}

	// Parse LIMIT
	var limit int
	if limitStr != "" {
		limit, err = strconv.Atoi(strings.TrimSpace(limitStr))
		if err != nil || limit < 0 {
			return nil, fmt.Errorf("invalid LIMIT value: %s", limitStr)
		}
	}

	return &Query{
		Algorithm:       algorithm,
		Path:            path,
		Conditions:      conditions,
		ConditionGroups: conditionGroups,
		ReturnItems:     returnItems,
		Distinct:        distinct,
		OrderBy:         orderBy,
		Limit:           limit,
		RawQuery:        originalQuery,
	}, nil
}

// findKeyword finds the index of a keyword in uppercase string, respecting word boundaries
func findKeyword(s string, keyword string) int {
	idx := 0
	for {
		pos := strings.Index(s[idx:], keyword)
		if pos == -1 {
			return -1
		}
		absPos := idx + pos
		// Check word boundary before
		if absPos > 0 && s[absPos-1] != ' ' && s[absPos-1] != ')' {
			idx = absPos + 1
			continue
		}
		// Check word boundary after
		afterPos := absPos + len(keyword)
		if afterPos < len(s) && s[afterPos] != ' ' && s[afterPos] != '(' {
			idx = absPos + 1
			continue
		}
		return absPos
	}
}

// parsePath parses the path pattern like (u:User)-[r:FOLLOWS]->(f:User)
func (p *Parser) parsePath(pathStr string) ([]PathElement, error) {
	var elements []PathElement

	// Pattern for node: (var:Type {props}) or (var:Type) or (var)
	// Pattern for relationship: -[var:Type]->
	// Combined: node followed by optional relationship

	// Split by relationship arrows, keeping the parts
	// We'll use a state machine approach

	remaining := pathStr
	for remaining != "" {
		remaining = strings.TrimSpace(remaining)

		// Expect a node pattern starting with (
		if !strings.HasPrefix(remaining, "(") {
			return nil, fmt.Errorf("expected node pattern starting with '(' at: %s", remaining)
		}

		// Find matching closing paren (accounting for nested braces)
		nodeEnd := p.findClosingParen(remaining)
		if nodeEnd == -1 {
			return nil, fmt.Errorf("unclosed parenthesis in node pattern")
		}

		nodeStr := remaining[1:nodeEnd] // content inside ()
		remaining = strings.TrimSpace(remaining[nodeEnd+1:])

		node, err := p.parseNode(nodeStr)
		if err != nil {
			return nil, err
		}

		element := PathElement{Node: node}

		// Check for relationship patterns:
		// -[r]->  outgoing
		// <-[r]-  incoming
		// -[r]-   bidirectional (undirected)
		// <-[r]-> bidirectional (both directions)
		if strings.HasPrefix(remaining, "-[") || strings.HasPrefix(remaining, "<-[") {
			incoming := strings.HasPrefix(remaining, "<-[")
			if incoming {
				remaining = remaining[3:] // skip "<-["
			} else {
				remaining = remaining[2:] // skip "-["
			}

			// Find the closing ]
			bracketEnd := strings.Index(remaining, "]")
			if bracketEnd == -1 {
				return nil, fmt.Errorf("unclosed relationship pattern, expected ]")
			}

			relStr := remaining[:bracketEnd]
			remaining = remaining[bracketEnd+1:]

			// Determine direction from suffix
			var direction RelDirection
			if strings.HasPrefix(remaining, "->") {
				if incoming {
					direction = RelBidirectional // <-[r]->
				} else {
					direction = RelOutgoing // -[r]->
				}
				remaining = strings.TrimSpace(remaining[2:])
			} else if strings.HasPrefix(remaining, "-") {
				if incoming {
					direction = RelIncoming // <-[r]-
				} else {
					direction = RelBidirectional // -[r]-
				}
				remaining = strings.TrimSpace(remaining[1:])
			} else {
				return nil, fmt.Errorf("invalid relationship pattern ending, expected -> or -")
			}

			rel, err := p.parseRelationship(relStr)
			if err != nil {
				return nil, err
			}
			rel.Direction = direction
			element.Relationship = &rel
		}

		elements = append(elements, element)

		// If there was a relationship, we expect another node
		if element.Relationship != nil && remaining == "" {
			return nil, fmt.Errorf("relationship without target node")
		}
	}

	if len(elements) == 0 {
		return nil, fmt.Errorf("empty path pattern")
	}

	return elements, nil
}

// findClosingParen finds the index of the closing ) for an opening ( at position 0
func (p *Parser) findClosingParen(s string) int {
	depth := 0
	inBrace := 0
	for i, c := range s {
		switch c {
		case '(':
			if inBrace == 0 {
				depth++
			}
		case ')':
			if inBrace == 0 {
				depth--
				if depth == 0 {
					return i
				}
			}
		case '{':
			inBrace++
		case '}':
			inBrace--
		}
	}
	return -1
}

// parseNode parses a node pattern like "u:User {id: 123}" or "u:User" or "u"
func (p *Parser) parseNode(nodeStr string) (NodePattern, error) {
	node := NodePattern{
		Properties: make(map[string]interface{}),
	}

	// Check for inline properties
	propsStart := strings.Index(nodeStr, "{")
	var mainPart string
	if propsStart != -1 {
		propsEnd := strings.LastIndex(nodeStr, "}")
		if propsEnd == -1 {
			return node, fmt.Errorf("unclosed properties in node")
		}
		propsStr := nodeStr[propsStart+1 : propsEnd]
		mainPart = strings.TrimSpace(nodeStr[:propsStart])

		props, err := p.parseProperties(propsStr)
		if err != nil {
			return node, err
		}
		node.Properties = props
	} else {
		mainPart = strings.TrimSpace(nodeStr)
	}

	// Parse var:Type or just var
	if strings.Contains(mainPart, ":") {
		parts := strings.SplitN(mainPart, ":", 2)
		node.Variable = strings.TrimSpace(parts[0])
		node.Type = strings.TrimSpace(parts[1])
	} else {
		node.Variable = strings.TrimSpace(mainPart)
	}

	if node.Variable == "" {
		return node, fmt.Errorf("node variable is required")
	}

	return node, nil
}

// parseRelationship parses a relationship pattern like "r:FOLLOWS*1..5" or ":FOLLOWS" or "r"
func (p *Parser) parseRelationship(relStr string) (RelPattern, error) {
	rel := RelPattern{
		MinHops: 1,
		MaxHops: 1,
	}
	relStr = strings.TrimSpace(relStr)

	if relStr == "" {
		return rel, nil // Any relationship, single hop
	}

	// Check for variable-length pattern: *min..max, *..max, *min.., or *
	varLengthIdx := strings.Index(relStr, "*")
	if varLengthIdx != -1 {
		rel.IsVariable = true
		
		// Split into type part and range part
		typePart := strings.TrimSpace(relStr[:varLengthIdx])
		rangePart := strings.TrimSpace(relStr[varLengthIdx+1:])

		// Parse the type part (before *)
		if typePart != "" {
			if strings.Contains(typePart, ":") {
				parts := strings.SplitN(typePart, ":", 2)
				rel.Variable = strings.TrimSpace(parts[0])
				rel.Type = strings.TrimSpace(parts[1])
			} else if len(typePart) > 0 && typePart[0] >= 'A' && typePart[0] <= 'Z' {
				rel.Type = typePart
			} else {
				rel.Variable = typePart
			}
		}

		// Parse the range part (after *)
		if rangePart == "" {
			// Just *, means 1 to unlimited
			rel.MinHops = 1
			rel.MaxHops = 0 // 0 means use max_depth
		} else if strings.Contains(rangePart, "..") {
			// Parse min..max
			rangeParts := strings.SplitN(rangePart, "..", 2)
			minStr := strings.TrimSpace(rangeParts[0])
			maxStr := strings.TrimSpace(rangeParts[1])

			if minStr != "" {
				min, err := strconv.Atoi(minStr)
				if err != nil {
					return rel, fmt.Errorf("invalid min hops: %s", minStr)
				}
				rel.MinHops = min
			} else {
				rel.MinHops = 1 // Default min is 1
			}

			if maxStr != "" {
				max, err := strconv.Atoi(maxStr)
				if err != nil {
					return rel, fmt.Errorf("invalid max hops: %s", maxStr)
				}
				rel.MaxHops = max
			} else {
				rel.MaxHops = 0 // 0 means unlimited (use max_depth)
			}
		} else {
			// Just a number: *3 means exactly 3 hops
			exact, err := strconv.Atoi(rangePart)
			if err != nil {
				return rel, fmt.Errorf("invalid hop count: %s", rangePart)
			}
			rel.MinHops = exact
			rel.MaxHops = exact
		}

		// Validate
		if rel.MinHops < 0 {
			return rel, fmt.Errorf("min hops cannot be negative")
		}
		if rel.MaxHops != 0 && rel.MaxHops < rel.MinHops {
			return rel, fmt.Errorf("max hops cannot be less than min hops")
		}

		return rel, nil
	}

	// No variable-length, parse normally
	if strings.Contains(relStr, ":") {
		parts := strings.SplitN(relStr, ":", 2)
		rel.Variable = strings.TrimSpace(parts[0])
		rel.Type = strings.TrimSpace(parts[1])
	} else {
		// Could be just a variable or just a type
		// Convention: if it starts with uppercase, it's a type
		if len(relStr) > 0 && relStr[0] >= 'A' && relStr[0] <= 'Z' {
			rel.Type = relStr
		} else {
			rel.Variable = relStr
		}
	}

	return rel, nil
}

// parseProperties parses inline properties like "id: 123, name: 'Alice'"
func (p *Parser) parseProperties(propsStr string) (map[string]interface{}, error) {
	props := make(map[string]interface{})

	// Split by comma, but be careful with quoted strings
	pairs := p.splitProperties(propsStr)

	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		colonIdx := strings.Index(pair, ":")
		if colonIdx == -1 {
			return nil, fmt.Errorf("invalid property format: %s", pair)
		}

		key := strings.TrimSpace(pair[:colonIdx])
		key = strings.Trim(key, "\"'")
		valueStr := strings.TrimSpace(pair[colonIdx+1:])

		value := p.parseValue(valueStr)
		props[key] = value
	}

	return props, nil
}

// splitProperties splits property pairs by comma, respecting quotes
func (p *Parser) splitProperties(s string) []string {
	var result []string
	var current strings.Builder
	inQuote := rune(0)

	for _, c := range s {
		if inQuote != 0 {
			current.WriteRune(c)
			if c == inQuote {
				inQuote = 0
			}
		} else if c == '"' || c == '\'' {
			inQuote = c
			current.WriteRune(c)
		} else if c == ',' {
			result = append(result, current.String())
			current.Reset()
		} else {
			current.WriteRune(c)
		}
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result
}

// parseValue parses a value string into appropriate Go type
func (p *Parser) parseValue(valueStr string) interface{} {
	valueStr = strings.TrimSpace(valueStr)

	// Check for quoted string
	if (strings.HasPrefix(valueStr, "\"") && strings.HasSuffix(valueStr, "\"")) ||
		(strings.HasPrefix(valueStr, "'") && strings.HasSuffix(valueStr, "'")) {
		return valueStr[1 : len(valueStr)-1]
	}

	// Check for boolean
	lower := strings.ToLower(valueStr)
	if lower == "true" {
		return true
	}
	if lower == "false" {
		return false
	}

	// Check for null
	if lower == "null" || lower == "nil" {
		return nil
	}

	// Check for integer
	if i, err := strconv.ParseInt(valueStr, 10, 64); err == nil {
		return int(i)
	}

	// Check for float
	if f, err := strconv.ParseFloat(valueStr, 64); err == nil {
		return f
	}

	// Default to string
	return valueStr
}

// parseWhereWithOr parses WHERE conditions with OR support
// e.g., "u.name = 'Alice' AND u.age > 30 OR u.name = 'Bob'"
func (p *Parser) parseWhereWithOr(whereStr string) ([]ConditionGroup, error) {
	var groups []ConditionGroup

	// Split by OR (case insensitive)
	orParts := regexp.MustCompile(`(?i)\s+OR\s+`).Split(whereStr, -1)

	for _, orPart := range orParts {
		orPart = strings.TrimSpace(orPart)
		if orPart == "" {
			continue
		}

		// Each OR part contains AND-joined conditions
		conditions, err := p.parseWhere(orPart)
		if err != nil {
			return nil, err
		}

		if len(conditions) > 0 {
			groups = append(groups, ConditionGroup{Conditions: conditions})
		}
	}

	return groups, nil
}

// parseOrderBy parses ORDER BY clause like "u.name ASC, u.age DESC"
func (p *Parser) parseOrderBy(orderByStr string) ([]OrderByItem, error) {
	var items []OrderByItem

	parts := strings.Split(orderByStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		item := OrderByItem{
			Direction: OrderAsc, // Default
		}

		// Check for ASC/DESC suffix
		upperPart := strings.ToUpper(part)
		if strings.HasSuffix(upperPart, " DESC") {
			item.Direction = OrderDesc
			part = strings.TrimSpace(part[:len(part)-5])
		} else if strings.HasSuffix(upperPart, " ASC") {
			item.Direction = OrderAsc
			part = strings.TrimSpace(part[:len(part)-4])
		}

		item.VarPath = part
		if item.VarPath == "" {
			return nil, fmt.Errorf("empty ORDER BY item")
		}

		items = append(items, item)
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("empty ORDER BY clause")
	}

	return items, nil
}

// parseWhere parses WHERE conditions like "u.name = 'Alice' AND u.age > 30"
func (p *Parser) parseWhere(whereStr string) ([]Condition, error) {
	var conditions []Condition

	// Split by AND (case insensitive)
	parts := regexp.MustCompile(`(?i)\s+AND\s+`).Split(whereStr, -1)

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		cond, err := p.parseCondition(part)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, cond)
	}

	return conditions, nil
}

// parseCondition parses a single condition like "u.name = 'Alice'"
func (p *Parser) parseCondition(condStr string) (Condition, error) {
	// Match: varPath operator value
	// Operators: <=, >=, !=, =, <, >
	pattern := regexp.MustCompile(`^(.+?)\s*(<=|>=|!=|=|<|>)\s*(.+)$`)
	matches := pattern.FindStringSubmatch(condStr)
	if matches == nil {
		return Condition{}, fmt.Errorf("invalid condition: %s", condStr)
	}

	varPath := strings.TrimSpace(matches[1])
	opStr := matches[2]
	valueStr := strings.TrimSpace(matches[3])

	var op Operator
	switch opStr {
	case "=":
		op = OpEq
	case "!=":
		op = OpNe
	case "<":
		op = OpLt
	case ">":
		op = OpGt
	case "<=":
		op = OpLte
	case ">=":
		op = OpGte
	default:
		return Condition{}, fmt.Errorf("unknown operator: %s", opStr)
	}

	value := p.parseValue(valueStr)

	return Condition{
		VarPath:  varPath,
		Operator: op,
		Value:    value,
	}, nil
}

// parseReturn parses RETURN items like "u, f, u.name"
func (p *Parser) parseReturn(returnStr string) ([]ReturnItem, error) {
	var items []ReturnItem

	parts := strings.Split(returnStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		item := ReturnItem{}
		if strings.Contains(part, ".") {
			dotParts := strings.SplitN(part, ".", 2)
			item.Variable = strings.TrimSpace(dotParts[0])
			item.Property = strings.TrimSpace(dotParts[1])
		} else {
			item.Variable = part
		}

		items = append(items, item)
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("RETURN clause cannot be empty")
	}

	return items, nil
}
