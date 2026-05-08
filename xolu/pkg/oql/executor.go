// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ha1tch/tsqlparser/ast"
	"github.com/ha1tch/xolu/pkg/models"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/rs/zerolog/log"
)

// Sentinel errors for query limit violations.
var (
	ErrScanLimit   = errors.New("query scan limit exceeded")
	ErrResultLimit = errors.New("query result limit exceeded")
)

// Executor executes OQL queries against storage
type Executor struct {
	store           storage.Store
	aggregator      *Aggregator
	schemaValidator SchemaValidator
	planner         *Planner
	dialect         SQLDialect
	limits          QueryLimits
	// sqlTenantID is injected into push-down SQL WHERE clauses to filter by
	// the tenant_id column. It is set by ExecuteWithStore (which extracts it
	// from the store's config) and left empty otherwise. This is separate from
	// the tenantID parameter in ExecuteWithTenant, which also controls Go-path
	// filtering via filterByTenant. When a scoped store is used, the Go-path
	// doesn't need filtering (the store's List already returns scoped data),
	// but the push-down path does (QueryWithPlan runs raw SQL).
	sqlTenantID string
}

// QueryLimits holds server-enforced limits for query execution.
// Zero values mean "use default" (set by the server from config).
type QueryLimits struct {
	MaxRows     int // Max rows returned
	MaxScanRows int // Max rows scanned before abort
}

// NewExecutor creates a new executor
func NewExecutor(store storage.Store, sv SchemaValidator) *Executor {
	// Determine dialect from storage backend
	var dialect SQLDialect
	var planner *Planner
	if _, ok := store.(storage.Queryable); ok {
		dialect = &SQLiteDialect{} // Default — future backends provide their own dialect
		planner = NewPlannerFromDialect(dialect)
	} else {
		planner = NewPlanner() // Fallback threshold; won't matter since push-down requires Queryable
	}

	return &Executor{
		store:           store,
		aggregator:      NewAggregator(),
		schemaValidator: sv,
		planner:         planner,
		dialect:         dialect,
	}
}

// SetProfile updates the planner's hardware profile, which controls
// push-down thresholds for complex queries. Call this after NewExecutor
// and before serving requests — typically during server startup after
// calibration or profile selection.
func (e *Executor) SetProfile(profile *HardwareProfile) {
	if profile == nil {
		return
	}
	e.planner = NewPlannerWithProfile(e.dialect, profile)
}

// SetLimits configures query execution limits.
func (e *Executor) SetLimits(limits QueryLimits) {
	e.limits = limits
}

// Execute executes a validated AST statement
func (e *Executor) Execute(ctx context.Context, stmt ast.Statement) (*Result, error) {
	return e.ExecuteWithTenant(ctx, stmt, "")
}

// ExecuteWithStore executes a validated AST statement using a specific store.
// This is the preferred method for tenant-scoped queries: the caller passes
// a store already scoped to the tenant. The store's TenantID is extracted
// and set as sqlTenantID so that push-down queries include a tenant_id
// WHERE clause. The Go-path does not need additional filtering because
// the store's List/Search methods already return only that tenant's data.
func (e *Executor) ExecuteWithStore(ctx context.Context, stmt ast.Statement, store storage.Store) (*Result, error) {
	// Extract tenant ID for SQL generation (push-down path only).
	sqlTenantID := ""
	if cfg := store.Config(); cfg.TenantID != 0 {
		sqlTenantID = fmt.Sprintf("%d", cfg.TenantID)
	}
	// Create a temporary executor with the overridden store.
	tmp := &Executor{
		store:           store,
		aggregator:      e.aggregator,
		schemaValidator: e.schemaValidator,
		planner:         e.planner,
		dialect:         e.dialect,
		limits:          e.limits,
		sqlTenantID:     sqlTenantID,
	}
	// Pass empty tenantID: Go-path filtering is unnecessary because
	// the store is already scoped. sqlTenantID handles the push-down path.
	return tmp.ExecuteWithTenant(ctx, stmt, "")
}

// ExecuteWithTenant executes a validated AST statement with tenant scoping.
// When tenantID is non-empty, all operations are filtered to records where
// the tenant_id field matches. This ensures OQL queries respect tenant isolation.
func (e *Executor) ExecuteWithTenant(ctx context.Context, stmt ast.Statement, tenantID string) (*Result, error) {
	switch s := stmt.(type) {
	case *ast.SelectStatement:
		return e.executeSelect(ctx, s, tenantID)
	case *ast.InsertStatement:
		return e.executeInsert(ctx, s, tenantID)
	case *ast.UpdateStatement:
		return e.executeUpdate(ctx, s, tenantID)
	case *ast.DeleteStatement:
		return e.executeDelete(ctx, s, tenantID)
	default:
		return nil, fmt.Errorf("unsupported statement: %T", stmt)
	}
}

func (e *Executor) executeSelect(ctx context.Context, s *ast.SelectStatement, tenantID string) (*Result, error) {
	startTime := time.Now()

	// Extract entity name
	entity := extractEntityFromSelect(s)

	// Plan: decide which operations to push down to storage
	plan := QueryPlan{Push: []PushDecision{PushNone}}
	if e.planner != nil {
		plan = e.planner.Plan(ctx, s, e.store)
	}

	// Validate that the Go path can handle the WHERE clause if we're not
	// pushing down the full WHERE. This fails early with a clear error
	// rather than silently returning empty results.
	if s.Where != nil && !plan.pushed(PushWhere) {
		if err := isGoPathSupported(s.Where); err != nil {
			return nil, err
		}
	}

	var records []map[string]interface{}
	var scanned int
	var err error

	// Extract the set of fields referenced by the query. Used to enable
	// selective field extraction (jsonic) on blob entities when possible.
	queryFields, isSelectStar := extractQueryFields(s)

	// Resolve the tenant ID for push-down SQL.
	sqlTID := tenantID
	if e.sqlTenantID != "" {
		sqlTID = e.sqlTenantID
	}

	// The planner has already decided which strategy to use. The executor
	// dispatches on the plan and falls back to the Go path on any error.
	//
	// Strategies (in priority order):
	//   PushFull      — entire SELECT as one SQL statement (adapted tables)
	//   PushAggregate — GROUP BY + aggregates in SQL, HAVING in Go (adapted)
	//   PushWhere+    — WHERE/ORDER BY/LIMIT in SQL (blob entities)
	//   Go path       — fetch all, filter/sort/aggregate in Go
	fetched := false

	switch {
	case plan.pushed(PushFull):
		// Full adapted-table push-down: translates the entire query (SELECT,
		// WHERE, GROUP BY, HAVING, ORDER BY, DISTINCT, LIMIT) into a single
		// SQL statement against native columns.
		aggStore := e.store.(storage.AggregateQueryable)
		adaptedSQL, genErr := GenerateAdaptedSQL(s, entity, sqlTID, aggStore, e.dialect)
		if genErr != nil {
			log.Debug().Err(genErr).Msg("Full adapted SQL generation failed, falling back")
			break
		}
		adaptedRecords, queryErr := aggStore.AggregateQuery(ctx, adaptedSQL.SQL, adaptedSQL.Args, adaptedSQL.Aliases)
		if queryErr != nil {
			log.Debug().Err(queryErr).Str("sql", adaptedSQL.SQL).Msg("Full adapted push-down query failed, falling back")
			break
		}
		denormaliseAggregateDecimals(adaptedRecords, adaptedSQL.DecimalColumns, adaptedSQL.Aliases, s.Columns)
		records = adaptedRecords
		scanned = plan.EstimatedN
		fetched = true

	case plan.pushed(PushAggregate):
		// Aggregate push-down: replaces the fetch+aggregate pipeline with a
		// single SQL query, but still runs HAVING in Go.
		aggStore := e.store.(storage.AggregateQueryable)
		aggSQL, aggErr := GenerateAggregateSQL(s, entity, sqlTID, aggStore, e.dialect)
		if aggErr != nil {
			log.Debug().Err(aggErr).Msg("Aggregate SQL generation failed, falling back")
			break
		}
		aggRecords, queryErr := aggStore.AggregateQuery(ctx, aggSQL.SQL, aggSQL.Args, aggSQL.Aliases)
		if queryErr != nil {
			log.Debug().Err(queryErr).Str("sql", aggSQL.SQL).Msg("Aggregate push-down query failed, falling back")
			break
		}
		denormaliseAggregateDecimals(aggRecords, aggSQL.DecimalColumns, aggSQL.Aliases, s.Columns)
		records = aggRecords
		scanned = plan.EstimatedN
		// Apply HAVING in Go (SQLite HAVING would need further translation).
		if s.Having != nil {
			records = filterRecordsByExpr(records, s.Having)
		}
		fetched = true

	case plan.hasPush() && e.dialect != nil:
		// Blob push-down: generate SQL for pushed operations (WHERE, ORDER BY,
		// LIMIT) against the entities table using json_extract().
		gen, genErr := GenerateSQL(s, entity, sqlTID, plan, e.dialect)
		if genErr != nil {
			// Fall back to Go path on generation failure.
			plan = QueryPlan{Push: []PushDecision{PushNone}}
			break
		}
		// Prefer QueryWithFields for selective extraction when available.
		if fq, ok := e.store.(storage.FieldQueryable); ok && !isSelectStar {
			records, err = fq.QueryWithFields(ctx, gen.SQL, gen.Args, queryFields)
		} else {
			queryable := e.store.(storage.Queryable)
			records, err = queryable.QueryWithPlan(ctx, gen.SQL, gen.Args)
		}
		if err != nil {
			return nil, fmt.Errorf("push-down query failed: %w", err)
		}
		scanned = plan.EstimatedN
		fetched = true
	}

	// Go path fallback: fetch all records, filter and sort in Go.
	if !fetched {
		// B4 path: if the store supports inline predicate filtering and the
		// WHERE clause has compilable terms, push predicates into the
		// tokenisation loop. This avoids allocating maps for rejected rows.
		var residualWhere ast.Expression
		if fs, ok := e.store.(storage.FilterableStore); ok && !isSelectStar && s.Where != nil {
			compiled := CompilePredicates(s.Where)
			if compiled.Preds != nil && compiled.Preds.Len() > 0 {
				records, err = fs.ListWithFieldsAndFilter(ctx, entity, queryFields, compiled.Preds)
				residualWhere = compiled.Residual
				fetched = true
			}
		}

		if !fetched {
			// Prefer ListWithFields for selective extraction when available.
			if fq, ok := e.store.(storage.FieldQueryable); ok && !isSelectStar {
				records, err = fq.ListWithFields(ctx, entity, queryFields)
			} else {
				records, err = e.store.List(ctx, entity)
			}
			residualWhere = s.Where
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read entity '%s': %w", entity, err)
		}
		scanned = len(records)

		// Enforce scan limit
		if e.limits.MaxScanRows > 0 && scanned > e.limits.MaxScanRows {
			return nil, fmt.Errorf("%w: scanned %d rows (max %d)", ErrScanLimit, scanned, e.limits.MaxScanRows)
		}

		// Check context deadline
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("query cancelled: %w", err)
		}

		// 1.5. Apply tenant filter if scoped
		records = filterByTenant(records, tenantID)

		// 2. Apply WHERE filter (residual only if B4 pushed some predicates)
		if residualWhere != nil {
			records = e.filterRecords(records, residualWhere)
		}
	}

	// Everything below runs regardless of path.
	// Operations already handled by push-down are skipped via plan checks.

	// 2.5. Pre-compute scalar functions used in SELECT or GROUP BY
	if !plan.pushed(PushFull) && !plan.pushed(PushAggregate) {
		records = e.materializeScalars(records, s.Columns, s.GroupBy)
	}

	// 3. GROUP BY + aggregates (skip if pushed to SQL)
	if !plan.pushed(PushFull) && !plan.pushed(PushAggregate) && (len(s.GroupBy) > 0 || hasAggregates(s.Columns)) {
		// Detect decimal columns for precise aggregation
		e.configureDecimalAggregation(entity)
		records = e.aggregator.Aggregate(records, s.Columns, s.GroupBy, s.Having)
	}

	// 4. ORDER BY (skip if pushed)
	if len(s.OrderBy) > 0 && !plan.pushed(PushFull) && !plan.pushed(PushOrderBy) {
		records = OrderBy(records, s.OrderBy)
	}

	// 5. DISTINCT (skip if full push-down handled it)
	if s.Distinct && !plan.pushed(PushFull) {
		records = e.distinctRecords(records, s.Columns)
	}

	// 6. TOP (skip if pushed)
	if s.Top != nil && !plan.pushed(PushFull) && !plan.pushed(PushLimit) {
		records = ApplyTop(records, s.Top)
	}

	// 7. Enforce row limit
	if e.limits.MaxRows > 0 && len(records) > e.limits.MaxRows {
		return nil, fmt.Errorf("%w: %d rows (max %d)", ErrResultLimit, len(records), e.limits.MaxRows)
	}

	// 8. Check context deadline before projection
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("query cancelled: %w", err)
	}

	// 9. Project columns
	rows := e.projectColumns(records, s.Columns)

	return NewSelectResult(rows, scanned, time.Since(startTime)), nil
}

func (e *Executor) executeInsert(ctx context.Context, s *ast.InsertStatement, tenantID string) (*Result, error) {
	startTime := time.Now()

	entity := normalizeEntityName(s.Table.String())
	inserted := 0

	// Extract column names
	var columns []string
	for _, col := range s.Columns {
		columns = append(columns, col.Value)
	}

	// Insert each row
	for i, row := range s.Values {
		record := make(map[string]interface{})

		if len(columns) > 0 {
			// Named columns
			for j, val := range row {
				if j < len(columns) {
					record[columns[j]] = evalLiteral(val)
				}
			}
		} else {
			// Positional - would need schema to know column names
			// For now, just number them
			for j, val := range row {
				record[fmt.Sprintf("col%d", j)] = evalLiteral(val)
			}
		}

		// Inject tenant_id if scoped
		if tenantID != "" {
			record["tenant_id"] = tenantID
		}

		// Validate against schema if validator is configured
		if e.schemaValidator != nil {
			if valid, errors := e.schemaValidator.Validate(entity, record); !valid {
				return nil, fmt.Errorf("validation failed at row %d: %v", i+1, errors)
			}
		}

		if _, err := e.store.Create(ctx, entity, record); err != nil {
			return nil, fmt.Errorf("insert failed at row %d: %w", i+1, err)
		}
		inserted++
	}

	return NewMutationResult(ResultInsert, inserted, time.Since(startTime)), nil
}

func (e *Executor) executeUpdate(ctx context.Context, s *ast.UpdateStatement, tenantID string) (*Result, error) {
	startTime := time.Now()

	entity := normalizeEntityName(s.Table.String())

	// Plan: decide whether to push WHERE to narrow the initial fetch
	plan := QueryPlan{Push: []PushDecision{PushNone}}
	if e.planner != nil {
		plan = e.planner.PlanMutation(ctx, s.Where, entity, e.store)
	}

	// Validate Go-path support for WHERE clause
	if s.Where != nil && !plan.pushed(PushWhere) {
		if err := isGoPathSupported(s.Where); err != nil {
			return nil, err
		}
	}

	var records []map[string]interface{}
	var err error

	if plan.pushed(PushWhere) && e.dialect != nil {
		// Build a synthetic SELECT to reuse GenerateSQL
		mutTID := tenantID
		if e.sqlTenantID != "" {
			mutTID = e.sqlTenantID
		}
		gen, genErr := generateMutationSQL(s.Where, entity, mutTID, e.dialect)
		if genErr == nil {
			queryable := e.store.(storage.Queryable)
			records, err = queryable.QueryWithPlan(ctx, gen.SQL, gen.Args)
			if err != nil {
				return nil, fmt.Errorf("push-down query failed: %w", err)
			}
		} else {
			// Fall back to Go path
			plan = QueryPlan{Push: []PushDecision{PushNone}}
		}
	}

	if !plan.pushed(PushWhere) {
		// Original Go path
		records, err = e.store.List(ctx, entity)
		if err != nil {
			return nil, fmt.Errorf("failed to read entity '%s': %w", entity, err)
		}
		records = filterByTenant(records, tenantID)
		records = e.filterRecords(records, s.Where)
	}

	updated := 0
	for _, rec := range records {
		id := extractID(rec)
		if id == 0 {
			continue
		}

		// Apply SET clauses
		for _, set := range s.SetClauses {
			colName := extractSetColumn(set)
			rec[colName] = evalLiteralWithRecord(set.Value, rec)
		}

		// Validate against schema if validator is configured
		if e.schemaValidator != nil {
			if valid, errors := e.schemaValidator.Validate(entity, rec); !valid {
				return nil, fmt.Errorf("validation failed for id %d: %v", id, errors)
			}
		}

		if err := e.store.Update(ctx, entity, id, rec); err != nil {
			return nil, fmt.Errorf("update failed for id %d: %w", id, err)
		}
		updated++
	}

	return NewMutationResult(ResultUpdate, updated, time.Since(startTime)), nil
}

func (e *Executor) executeDelete(ctx context.Context, s *ast.DeleteStatement, tenantID string) (*Result, error) {
	startTime := time.Now()

	entity := normalizeEntityName(extractDeleteEntity(s))

	// Plan: decide whether to push WHERE to narrow the initial fetch
	plan := QueryPlan{Push: []PushDecision{PushNone}}
	if e.planner != nil {
		plan = e.planner.PlanMutation(ctx, s.Where, entity, e.store)
	}

	// Validate Go-path support for WHERE clause
	if s.Where != nil && !plan.pushed(PushWhere) {
		if err := isGoPathSupported(s.Where); err != nil {
			return nil, err
		}
	}

	var records []map[string]interface{}
	var err error

	if plan.pushed(PushWhere) && e.dialect != nil {
		mutTID := tenantID
		if e.sqlTenantID != "" {
			mutTID = e.sqlTenantID
		}
		gen, genErr := generateMutationSQL(s.Where, entity, mutTID, e.dialect)
		if genErr == nil {
			queryable := e.store.(storage.Queryable)
			records, err = queryable.QueryWithPlan(ctx, gen.SQL, gen.Args)
			if err != nil {
				return nil, fmt.Errorf("push-down query failed: %w", err)
			}
		} else {
			plan = QueryPlan{Push: []PushDecision{PushNone}}
		}
	}

	if !plan.pushed(PushWhere) {
		records, err = e.store.List(ctx, entity)
		if err != nil {
			return nil, fmt.Errorf("failed to read entity '%s': %w", entity, err)
		}
		records = filterByTenant(records, tenantID)
		records = e.filterRecords(records, s.Where)
	}

	deleted := 0
	for _, rec := range records {
		id := extractID(rec)
		if id == 0 {
			continue
		}

		if err := e.store.Delete(ctx, entity, id); err != nil {
			return nil, fmt.Errorf("delete failed for id %d: %w", id, err)
		}
		deleted++
	}

	return NewMutationResult(ResultDelete, deleted, time.Since(startTime)), nil
}

// filterRecords filters records by a WHERE expression
func (e *Executor) filterRecords(records []map[string]interface{}, where ast.Expression) []map[string]interface{} {
	var filtered []map[string]interface{}
	for _, rec := range records {
		if e.evalCondition(rec, where) {
			filtered = append(filtered, rec)
		}
	}
	return filtered
}

// isGoPathSupported walks a WHERE expression and returns an error if it
// contains expression types that the Go-path evaluator cannot handle.
// This prevents silent empty results from fail-closed evalCondition.
func isGoPathSupported(expr ast.Expression) error {
	switch ex := expr.(type) {
	case *ast.InfixExpression:
		switch ex.Operator {
		case "AND", "OR":
			if err := isGoPathSupported(ex.Left); err != nil {
				return err
			}
			return isGoPathSupported(ex.Right)
		default:
			// Comparison operators are supported
			return nil
		}
	case *ast.PrefixExpression:
		if ex.Operator == "NOT" {
			return isGoPathSupported(ex.Right)
		}
		return fmt.Errorf("unsupported prefix operator in WHERE clause: %s", ex.Operator)
	case *ast.IsNullExpression:
		return nil
	case *ast.BetweenExpression:
		return nil
	case *ast.InExpression:
		return nil
	case *ast.LikeExpression:
		return nil
	default:
		return fmt.Errorf("unsupported expression type in WHERE clause: %T (query cannot be evaluated in Go path)", expr)
	}
}

// evalCondition evaluates a WHERE condition
func (e *Executor) evalCondition(rec map[string]interface{}, expr ast.Expression) bool {
	switch ex := expr.(type) {
	case *ast.InfixExpression:
		switch ex.Operator {
		case "AND":
			return e.evalCondition(rec, ex.Left) && e.evalCondition(rec, ex.Right)
		case "OR":
			return e.evalCondition(rec, ex.Left) || e.evalCondition(rec, ex.Right)
		default:
			left := e.evalExpr(rec, ex.Left)
			right := e.evalExpr(rec, ex.Right)
			return evalComparison(left, ex.Operator, right)
		}

	case *ast.PrefixExpression:
		if ex.Operator == "NOT" {
			return !e.evalCondition(rec, ex.Right)
		}

	case *ast.IsNullExpression:
		val := e.evalExpr(rec, ex.Expr)
		isNull := val == nil
		return isNull != ex.Not

	case *ast.BetweenExpression:
		val := e.evalExpr(rec, ex.Expr)
		low := e.evalExpr(rec, ex.Low)
		high := e.evalExpr(rec, ex.High)
		inRange := compareValues(val, low) >= 0 && compareValues(val, high) <= 0
		return inRange != ex.Not

	case *ast.InExpression:
		val := e.evalExpr(rec, ex.Expr)
		found := false
		for _, item := range ex.Values {
			if compareValues(val, e.evalExpr(rec, item)) == 0 {
				found = true
				break
			}
		}
		return found != ex.Not

	case *ast.LikeExpression:
		val := fmt.Sprintf("%v", e.evalExpr(rec, ex.Expr))
		pattern := fmt.Sprintf("%v", e.evalExpr(rec, ex.Pattern))
		matches := matchLike(val, pattern)
		return matches != ex.Not
	}

	// Fail-closed: unsupported expression types reject the record rather than
	// accepting it. This prevents data leakage when the Go-path evaluator
	// encounters an expression it cannot evaluate (e.g. CASE, CAST, EXISTS,
	// subqueries, or any future parser additions). The push-down path handles
	// these via SQL generation; the Go path must be conservative.
	return false
}

// evalExpr evaluates an expression to a value
func (e *Executor) evalExpr(rec map[string]interface{}, expr ast.Expression) interface{} {
	switch ex := expr.(type) {
	case *ast.Identifier:
		// Handle TRUE/FALSE as booleans
		upper := strings.ToUpper(ex.Value)
		if upper == "TRUE" {
			return true
		}
		if upper == "FALSE" {
			return false
		}
		return getFieldValue(rec, ex.Value)
	case *ast.QualifiedIdentifier:
		return getFieldValue(rec, ex.String())
	case *ast.IntegerLiteral:
		return ex.Value
	case *ast.FloatLiteral:
		return ex.Value
	case *ast.StringLiteral:
		return ex.Value
	case *ast.NullLiteral:
		return nil
	case *ast.FunctionCall:
		// Check scalar functions first
		if IsScalarFunction(ex) {
			return EvalScalarFunction(ex, func(arg ast.Expression) interface{} {
				return e.evalExpr(rec, arg)
			})
		}
		// For aggregate functions or unknown, try looking up the result by
		// the stringified expression (already computed by the aggregator).
		key := exprToString(ex)
		if val, ok := rec[key]; ok {
			return val
		}
		return nil
	case *ast.InfixExpression:
		// Arithmetic
		left := e.evalExpr(rec, ex.Left)
		right := e.evalExpr(rec, ex.Right)
		return evalArithmetic(left, ex.Operator, right)
	}
	return nil
}

// materializeScalars pre-computes scalar function calls found in SELECT
// columns and GROUP BY expressions, storing results as virtual fields on
// each record. The field key is the expression's string representation
// (e.g., "DATE_TRUNC('hour', timestamp)"), which the aggregator and
// projectColumns use via getFieldValue to resolve values.
//
// Additionally, if a SELECT column has an alias (e.g., "as period"), the
// result is also stored under that alias so that GROUP BY and ORDER BY
// can reference it by name.
func (e *Executor) materializeScalars(records []map[string]interface{}, columns []ast.SelectColumn, groupBy []ast.Expression) []map[string]interface{} {
	// Collect scalar function expressions that need materialisation.
	type scalarEntry struct {
		key   string // exprToString — canonical virtual field name
		alias string // SELECT alias if any (empty otherwise)
		expr  *ast.FunctionCall
	}
	var scalars []scalarEntry

	for _, col := range columns {
		if fc, ok := col.Expression.(*ast.FunctionCall); ok && IsScalarFunction(fc) {
			alias := ""
			if col.Alias != nil {
				alias = col.Alias.Value
			}
			scalars = append(scalars, scalarEntry{
				key:   exprToString(fc),
				alias: alias,
				expr:  fc,
			})
		}
	}
	for _, gb := range groupBy {
		if fc, ok := gb.(*ast.FunctionCall); ok && IsScalarFunction(fc) {
			scalars = append(scalars, scalarEntry{
				key:  exprToString(fc),
				expr: fc,
			})
		}
	}

	if len(scalars) == 0 {
		return records
	}

	// Deduplicate by key
	seen := make(map[string]bool)
	var unique []scalarEntry
	for _, s := range scalars {
		if !seen[s.key] {
			seen[s.key] = true
			unique = append(unique, s)
		}
	}

	// Evaluate each scalar on every record
	for _, rec := range records {
		for _, s := range unique {
			val := EvalScalarFunction(s.expr, func(arg ast.Expression) interface{} {
				return e.evalExpr(rec, arg)
			})
			rec[s.key] = val
			// Also store under alias so GROUP BY / ORDER BY can find it by name
			if s.alias != "" {
				rec[s.alias] = val
			}
		}
	}

	return records
}

// distinctRecords removes duplicate rows
func (e *Executor) distinctRecords(records []map[string]interface{}, columns []ast.SelectColumn) []map[string]interface{} {
	seen := make(map[string]bool)
	var unique []map[string]interface{}

	for _, rec := range records {
		key := e.buildDistinctKey(rec, columns)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, rec)
		}
	}
	return unique
}

func (e *Executor) buildDistinctKey(rec map[string]interface{}, columns []ast.SelectColumn) string {
	var parts []string
	for _, col := range columns {
		val := e.evalExpr(rec, col.Expression)
		parts = append(parts, fmt.Sprintf("%v", val))
	}
	return strings.Join(parts, "|")
}

// projectColumns projects selected columns from records
func (e *Executor) projectColumns(records []map[string]interface{}, columns []ast.SelectColumn) []map[string]interface{} {
	// Check for SELECT *
	for _, col := range columns {
		if col.AllColumns {
			return records // Return all columns
		}
	}

	var projected []map[string]interface{}
	for _, rec := range records {
		row := make(map[string]interface{})
		for _, col := range columns {
			alias := columnAlias(col)

			// If already computed (e.g., aggregate), use directly
			if val, ok := rec[alias]; ok {
				row[alias] = val
				continue
			}

			// Otherwise evaluate expression
			row[alias] = e.evalExpr(rec, col.Expression)
		}
		projected = append(projected, row)
	}
	return projected
}

// Helper functions

func extractEntityFromSelect(s *ast.SelectStatement) string {
	if s.From == nil || len(s.From.Tables) == 0 {
		return ""
	}
	if tn, ok := s.From.Tables[0].(*ast.TableName); ok {
		return normalizeEntityName(tn.Name.String())
	}
	return ""
}

func extractID(rec map[string]interface{}) int {
	if id, ok := rec["id"].(float64); ok {
		return int(id)
	}
	if id, ok := rec["id"].(int); ok {
		return id
	}
	if id, ok := rec["id"].(int64); ok {
		return int(id)
	}
	return 0
}

func extractSetColumn(set *ast.SetClause) string {
	if set.Column != nil {
		return set.Column.String()
	}
	return ""
}

func evalLiteral(expr ast.Expression) interface{} {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		return e.Value
	case *ast.FloatLiteral:
		return e.Value
	case *ast.StringLiteral:
		return e.Value
	case *ast.Identifier:
		// Handle TRUE/FALSE as booleans
		upper := strings.ToUpper(e.Value)
		if upper == "TRUE" {
			return true
		}
		if upper == "FALSE" {
			return false
		}
		return e.Value
	case *ast.NullLiteral:
		return nil
	case *ast.FunctionCall:
		// @REF('entity', id) — construct a structured REF object for graph edge indexing.
		// The parser represents @REF('author', 1) as a FunctionCall whose Function field
		// is a *ast.Variable named "@REF". Arguments: [0] entity name, [1] integer id.
		if v, ok := e.Function.(*ast.Variable); ok {
			switch strings.ToUpper(v.Name) {
			case "@REF":
				// @REF('entity', id) — construct a typed Reference and return its
				// canonical map form for JSON storage.
				if len(e.Arguments) == 2 {
					entity := strings.Trim(e.Arguments[0].String(), "'\"")
					if idVal, ok := evalLiteral(e.Arguments[1]).(int64); ok {
						return models.NewReference(entity, int64(idVal)).ToMap()
					}
				}
			case "@REFS":
				// @REFS(@REF(...), @REF(...), ...) — evaluate each argument as a
				// REF and collect into a []interface{} slice. syncGraphEdges will
				// create one graph edge per element via models.ExtractRefs.
				refs := make([]interface{}, 0, len(e.Arguments))
				for _, arg := range e.Arguments {
					if val := evalLiteral(arg); val != nil {
						refs = append(refs, val)
					}
				}
				return refs
			}
		}
		return expr.String()
	default:
		return expr.String()
	}
}

func evalLiteralWithRecord(expr ast.Expression, rec map[string]interface{}) interface{} {
	switch e := expr.(type) {
	case *ast.Identifier:
		return getFieldValue(rec, e.Value)
	case *ast.QualifiedIdentifier:
		return getFieldValue(rec, e.String())
	default:
		return evalLiteral(expr)
	}
}

func evalComparison(left interface{}, op string, right interface{}) bool {
	cmp := compareValues(left, right)
	switch op {
	case "=":
		return cmp == 0
	case "!=", "<>":
		return cmp != 0
	case "<":
		return cmp < 0
	case ">":
		return cmp > 0
	case "<=":
		return cmp <= 0
	case ">=":
		return cmp >= 0
	}
	return false
}

func evalArithmetic(left interface{}, op string, right interface{}) interface{} {
	l := toFloat(left)
	r := toFloat(right)
	switch op {
	case "+":
		return l + r
	case "-":
		return l - r
	case "*":
		return l * r
	case "/":
		if r == 0 {
			return nil
		}
		return l / r
	case "%":
		if r == 0 {
			return nil
		}
		return float64(int(l) % int(r))
	}
	return nil
}

// matchLike implements SQL LIKE pattern matching
// % = any characters, _ = single character
func matchLike(value, pattern string) bool {
	// Convert SQL LIKE pattern to simple matching
	// This is a simplified implementation
	pattern = strings.ToLower(pattern)
	value = strings.ToLower(value)

	// Handle % wildcards
	if strings.HasPrefix(pattern, "%") && strings.HasSuffix(pattern, "%") {
		return strings.Contains(value, pattern[1:len(pattern)-1])
	}
	if strings.HasPrefix(pattern, "%") {
		return strings.HasSuffix(value, pattern[1:])
	}
	if strings.HasSuffix(pattern, "%") {
		return strings.HasPrefix(value, pattern[:len(pattern)-1])
	}

	// Exact match (with _ handling could be added)
	return value == pattern
}

// filterByTenant filters records to those matching the given tenant ID.
// If tenantID is empty, all records are returned (no filtering).
// Matching is done by comparing the "tenant_id" field in each record.
func filterByTenant(records []map[string]interface{}, tenantID string) []map[string]interface{} {
	if tenantID == "" {
		return records
	}
	var filtered []map[string]interface{}
	for _, rec := range records {
		if tid, ok := rec["tenant_id"]; ok {
			if fmt.Sprintf("%v", tid) == tenantID {
				filtered = append(filtered, rec)
			}
		}
	}
	return filtered
}

// configureDecimalAggregation checks the store's adapted table registry for
// decimal columns in the given entity. If found, it configures the aggregator
// to use shopspring/decimal for those fields.
func (e *Executor) configureDecimalAggregation(entity string) {
	// Check if the store exposes an adapted table registry
	type adaptedRegistryProvider interface {
		AdaptedRegistry() *storage.AdaptedRegistry
	}

	provider, ok := e.store.(adaptedRegistryProvider)
	if !ok {
		e.aggregator.SetDecimalFields(nil)
		return
	}

	registry := provider.AdaptedRegistry()
	if registry == nil {
		e.aggregator.SetDecimalFields(nil)
		return
	}

	spec := registry.Get(entity)
	if spec == nil {
		e.aggregator.SetDecimalFields(nil)
		return
	}

	decimalFields := make(map[string]bool)
	for _, col := range spec.Columns {
		if col.Format == "decimal" {
			decimalFields[col.JSONField] = true
		}
	}

	if len(decimalFields) == 0 {
		e.aggregator.SetDecimalFields(nil)
	} else {
		e.aggregator.SetDecimalFields(decimalFields)
	}
}
