package reporting

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	metering "github.com/operator-framework/operator-metering/pkg/apis/metering/v1alpha1"
	meteringClient "github.com/operator-framework/operator-metering/pkg/generated/clientset/versioned/typed/metering/v1alpha1"
	meteringListers "github.com/operator-framework/operator-metering/pkg/generated/listers/metering/v1alpha1"
)

const maxDepth = 100

type ReportGenerationQueryDependencies struct {
	ReportGenerationQueries        []*metering.ReportGenerationQuery
	DynamicReportGenerationQueries []*metering.ReportGenerationQuery
	ReportDataSources              []*metering.ReportDataSource
	Reports                        []*metering.Report
}

func GetAndValidateGenerationQueryDependencies(
	queryGetter reportGenerationQueryGetter,
	dataSourceGetter reportDataSourceGetter,
	reportGetter reportGetter,
	generationQuery *metering.ReportGenerationQuery,
	handler *UninitialiedDependendenciesHandler,
) (*ReportGenerationQueryDependencies, error) {

	deps, err := GetGenerationQueryDependencies(
		queryGetter,
		dataSourceGetter,
		reportGetter,
		generationQuery,
	)
	if err != nil {
		return nil, err
	}
	err = ValidateGenerationQueryDependencies(deps, handler)
	if err != nil {
		return nil, err
	}
	return deps, nil
}

type UninitialiedDependendenciesHandler struct {
	HandleUninitializedReportGenerationQuery func(*metering.ReportGenerationQuery)
	HandleUninitializedReportDataSource      func(*metering.ReportDataSource)
}

func ValidateGenerationQueryDependencies(deps *ReportGenerationQueryDependencies, handler *UninitialiedDependendenciesHandler) error {
	// if the specified ReportGenerationQuery depends on other non-dynamic
	// ReportGenerationQueries, but they have their view disabled, then it's an
	// invalid configuration.
	var (
		uninitializedQueries     []*metering.ReportGenerationQuery
		uninitializedDataSources []*metering.ReportDataSource
	)
	validationErr := new(reportGenerationQueryDependenciesValidationError)
	for _, query := range deps.ReportGenerationQueries {
		// it's invalid for a ReportGenerationQuery with view.disabled set to
		// true to be a non-dynamic ReportGenerationQuery dependency
		if query.Spec.View.Disabled {
			validationErr.disabledViewQueryNames = append(validationErr.disabledViewQueryNames, query.Name)
			continue
		}
		// if a query doesn't disable view creation, than it is
		// uninitialized if it's view is not created/set yet
		if !query.Spec.View.Disabled && query.Status.ViewName == "" {
			uninitializedQueries = append(uninitializedQueries, query)
			validationErr.uninitializedQueryNames = append(validationErr.uninitializedQueryNames, query.Name)
		}
	}
	// anything below missing tableName in it's status is uninitialized
	for _, ds := range deps.ReportDataSources {
		if ds.Status.TableName == "" {
			uninitializedDataSources = append(uninitializedDataSources, ds)
			validationErr.uninitializedDataSourceNames = append(validationErr.uninitializedDataSourceNames, ds.Name)
		}
	}
	for _, report := range deps.Reports {
		if report.Status.TableName == "" {
			validationErr.uninitializedReportNames = append(validationErr.uninitializedReportNames, report.Name)
		}
	}

	if handler != nil {
		for _, query := range uninitializedQueries {
			handler.HandleUninitializedReportGenerationQuery(query)
		}

		for _, dataSource := range uninitializedDataSources {
			handler.HandleUninitializedReportDataSource(dataSource)
		}
	}

	if len(validationErr.disabledViewQueryNames) != 0 ||
		len(validationErr.uninitializedQueryNames) != 0 ||
		len(validationErr.uninitializedDataSourceNames) != 0 ||
		len(validationErr.uninitializedReportNames) != 0 ||
		len(validationErr.uninitializedReportNames) != 0 {
		return validationErr
	}
	return nil
}

func IsUninitializedDependencyError(err error) bool {
	validationErr, ok := err.(*reportGenerationQueryDependenciesValidationError)
	return ok && (len(validationErr.uninitializedQueryNames) != 0 ||
		len(validationErr.uninitializedDataSourceNames) != 0 ||
		len(validationErr.uninitializedReportNames) != 0 ||
		len(validationErr.uninitializedReportNames) != 0)
}

func IsInvalidDependencyError(err error) bool {
	validationErr, ok := err.(*reportGenerationQueryDependenciesValidationError)
	return ok && len(validationErr.disabledViewQueryNames) != 0
}

type reportGenerationQueryDependenciesValidationError struct {
	uninitializedQueryNames,
	disabledViewQueryNames,
	uninitializedDataSourceNames,
	uninitializedReportNames []string
}

func (e *reportGenerationQueryDependenciesValidationError) Error() string {
	var errs []string
	if len(e.uninitializedDataSourceNames) != 0 {
		errs = append(errs, fmt.Sprintf("uninitialized ReportDataSource dependencies: %s", strings.Join(e.uninitializedDataSourceNames, ", ")))
	}
	if len(e.disabledViewQueryNames) != 0 {
		errs = append(errs, fmt.Sprintf("invalid ReportGenerationQuery dependencies (disabled view): %s", strings.Join(e.disabledViewQueryNames, ", ")))
	}
	if len(e.uninitializedQueryNames) != 0 {
		errs = append(errs, fmt.Sprintf("uninitialized ReportGenerationQuery dependencies: %s", strings.Join(e.uninitializedQueryNames, ", ")))
	}
	if len(e.uninitializedReportNames) != 0 {
		errs = append(errs, fmt.Sprintf("uninitialized Report dependencies: %s", strings.Join(e.uninitializedReportNames, ", ")))
	}
	if len(e.uninitializedReportNames) != 0 {
		errs = append(errs, fmt.Sprintf("uninitialized Report dependencies: %s", strings.Join(e.uninitializedReportNames, ", ")))
	}

	if len(errs) != 0 {
		return fmt.Sprintf("ReportGenerationQueryDependencyValidationError: %s", strings.Join(errs, ", "))
	}
	panic("zero uninitialized or invalid dependencies")
}

func GetGenerationQueryDependencies(
	queryGetter reportGenerationQueryGetter,
	dataSourceGetter reportDataSourceGetter,
	reportGetter reportGetter,
	generationQuery *metering.ReportGenerationQuery,
) (*ReportGenerationQueryDependencies, error) {
	dataSourceDeps, err := GetDependentDataSources(dataSourceGetter, generationQuery)
	if err != nil {
		return nil, err
	}
	viewQueries, dynamicQueries, queriesDataSources, err := GetDependentGenerationQueries(queryGetter, dataSourceGetter, generationQuery)
	if err != nil {
		return nil, err
	}

	allDataSources := [][]*metering.ReportDataSource{
		dataSourceDeps,
		queriesDataSources,
	}

	// deduplicate the list of ReportDataSources
	seen := make(map[string]struct{})
	var dataSources []*metering.ReportDataSource
	for _, dsList := range allDataSources {
		for _, ds := range dsList {
			if _, exists := seen[ds.Name]; exists {
				continue
			}
			dataSources = append(dataSources, ds)
			seen[ds.Name] = struct{}{}
		}
	}

	reports, err := GetDependentReports(reportGetter, generationQuery)
	if err != nil {
		return nil, err
	}

	sort.Slice(viewQueries, func(i, j int) bool {
		return viewQueries[i].Name < viewQueries[j].Name
	})
	sort.Slice(dynamicQueries, func(i, j int) bool {
		return dynamicQueries[i].Name < dynamicQueries[j].Name
	})
	sort.Slice(dataSources, func(i, j int) bool {
		return dataSources[i].Name < dataSources[j].Name
	})
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Name < reports[j].Name
	})

	return &ReportGenerationQueryDependencies{
		ReportGenerationQueries:        viewQueries,
		DynamicReportGenerationQueries: dynamicQueries,
		ReportDataSources:              dataSources,
		Reports:                        reports,
	}, nil
}

func GetDependentGenerationQueries(queryGetter reportGenerationQueryGetter, dataSourceGetter reportDataSourceGetter, generationQuery *metering.ReportGenerationQuery) ([]*metering.ReportGenerationQuery, []*metering.ReportGenerationQuery, []*metering.ReportDataSource, error) {
	viewReportQueriesAccumulator := make(map[string]*metering.ReportGenerationQuery)
	dataSourcesAccumulator := make(map[string]*metering.ReportDataSource)
	dynamicReportQueriesAccumulator := make(map[string]*metering.ReportGenerationQuery)

	err := GetDependentGenerationQueriesWithDataSourcesMemoized(queryGetter, dataSourceGetter, generationQuery, 0, maxDepth, viewReportQueriesAccumulator, dynamicReportQueriesAccumulator, dataSourcesAccumulator)
	if err != nil {
		return nil, nil, nil, err
	}

	viewQueries := make([]*metering.ReportGenerationQuery, 0, len(viewReportQueriesAccumulator))
	for _, query := range viewReportQueriesAccumulator {
		viewQueries = append(viewQueries, query)
	}
	dynamicQueries := make([]*metering.ReportGenerationQuery, 0, len(dynamicReportQueriesAccumulator))
	for _, query := range dynamicReportQueriesAccumulator {
		dynamicQueries = append(dynamicQueries, query)
	}
	dataSources := make([]*metering.ReportDataSource, 0, len(dataSourcesAccumulator))
	for _, ds := range dataSourcesAccumulator {
		dataSources = append(dataSources, ds)
	}

	return viewQueries, dynamicQueries, dataSources, nil
}

type reportGenerationQueryGetter interface {
	getReportGenerationQuery(namespace, name string) (*metering.ReportGenerationQuery, error)
}

type reportGenerationQueryGetterFunc func(string, string) (*metering.ReportGenerationQuery, error)

func (f reportGenerationQueryGetterFunc) getReportGenerationQuery(namespace, name string) (*metering.ReportGenerationQuery, error) {
	return f(namespace, name)
}

func NewReportGenerationQueryListerGetter(lister meteringListers.ReportGenerationQueryLister) reportGenerationQueryGetter {
	return reportGenerationQueryGetterFunc(func(namespace, name string) (*metering.ReportGenerationQuery, error) {
		return lister.ReportGenerationQueries(namespace).Get(name)
	})
}

func NewReportGenerationQueryClientGetter(getter meteringClient.ReportGenerationQueriesGetter) reportGenerationQueryGetter {
	return reportGenerationQueryGetterFunc(func(namespace, name string) (*metering.ReportGenerationQuery, error) {
		return getter.ReportGenerationQueries(namespace).Get(name, metav1.GetOptions{})
	})
}

func GetDependentGenerationQueriesWithDataSourcesMemoized(queryGetter reportGenerationQueryGetter, dataSourceGetter reportDataSourceGetter, generationQuery *metering.ReportGenerationQuery, depth, maxDepth int, viewQueriesAccumulator, dynamicQueriesAccumulator map[string]*metering.ReportGenerationQuery, dataSourceAccumulator map[string]*metering.ReportDataSource) error {
	if depth >= maxDepth {
		return fmt.Errorf("detected a cycle at depth %d for generationQuery %s", depth, generationQuery.Name)
	}
	loopInput := []struct {
		accum      map[string]*metering.ReportGenerationQuery
		queryNames []string
	}{
		{
			accum:      viewQueriesAccumulator,
			queryNames: generationQuery.Spec.ReportQueries,
		},
		{
			accum:      dynamicQueriesAccumulator,
			queryNames: generationQuery.Spec.DynamicReportQueries,
		},
	}
	for _, input := range loopInput {
		for _, queryName := range input.queryNames {
			if _, exists := input.accum[queryName]; exists {
				continue
			}
			genQuery, err := queryGetter.getReportGenerationQuery(generationQuery.Namespace, queryName)
			if err != nil {
				return err
			}
			// get dependent ReportDataSources
			err = GetDependentDataSourcesMemoized(dataSourceGetter, genQuery, dataSourceAccumulator)
			if err != nil {
				return err
			}
			err = GetDependentGenerationQueriesWithDataSourcesMemoized(queryGetter, dataSourceGetter, genQuery, depth+1, maxDepth, viewQueriesAccumulator, dynamicQueriesAccumulator, dataSourceAccumulator)
			if err != nil {
				return err
			}
			input.accum[genQuery.Name] = genQuery
		}
	}
	return nil
}

type reportDataSourceGetter interface {
	getReportDataSource(namespace, name string) (*metering.ReportDataSource, error)
}

type reportDataSourceGetterFunc func(string, string) (*metering.ReportDataSource, error)

func (f reportDataSourceGetterFunc) getReportDataSource(namespace, name string) (*metering.ReportDataSource, error) {
	return f(namespace, name)
}

func NewReportDataSourceListerGetter(lister meteringListers.ReportDataSourceLister) reportDataSourceGetter {
	return reportDataSourceGetterFunc(func(namespace, name string) (*metering.ReportDataSource, error) {
		return lister.ReportDataSources(namespace).Get(name)
	})
}

func NewReportDataSourceClientGetter(getter meteringClient.ReportDataSourcesGetter) reportDataSourceGetter {
	return reportDataSourceGetterFunc(func(namespace, name string) (*metering.ReportDataSource, error) {
		return getter.ReportDataSources(namespace).Get(name, metav1.GetOptions{})
	})
}

func GetDependentDataSourcesMemoized(dataSourceGetter reportDataSourceGetter, generationQuery *metering.ReportGenerationQuery, dataSourceAccumulator map[string]*metering.ReportDataSource) error {
	for _, dataSourceName := range generationQuery.Spec.DataSources {
		if _, exists := dataSourceAccumulator[dataSourceName]; exists {
			continue
		}
		dataSource, err := dataSourceGetter.getReportDataSource(generationQuery.Namespace, dataSourceName)
		if err != nil {
			return err
		}
		dataSourceAccumulator[dataSource.Name] = dataSource
	}
	return nil
}

func GetDependentDataSources(dataSourceGetter reportDataSourceGetter, generationQuery *metering.ReportGenerationQuery) ([]*metering.ReportDataSource, error) {
	dataSourceAccumulator := make(map[string]*metering.ReportDataSource)
	err := GetDependentDataSourcesMemoized(dataSourceGetter, generationQuery, dataSourceAccumulator)
	if err != nil {
		return nil, err
	}
	dataSources := make([]*metering.ReportDataSource, 0, len(dataSourceAccumulator))
	for _, ds := range dataSourceAccumulator {
		dataSources = append(dataSources, ds)
	}
	return dataSources, nil
}

type reportGetter interface {
	getReport(namespace, name string) (*metering.Report, error)
}

type reportGetterFunc func(string, string) (*metering.Report, error)

func (f reportGetterFunc) getReport(namespace, name string) (*metering.Report, error) {
	return f(namespace, name)
}

func NewReportListerGetter(lister meteringListers.ReportLister) reportGetter {
	return reportGetterFunc(func(namespace, name string) (*metering.Report, error) {
		return lister.Reports(namespace).Get(name)
	})
}

func NewReportClientGetter(getter meteringClient.ReportsGetter) reportGetter {
	return reportGetterFunc(func(namespace, name string) (*metering.Report, error) {
		return getter.Reports(namespace).Get(name, metav1.GetOptions{})
	})
}

func GetDependentReports(reportGetter reportGetter, generationQuery *metering.ReportGenerationQuery) ([]*metering.Report, error) {
	reports := make([]*metering.Report, len(generationQuery.Spec.Reports))
	for i, reportName := range generationQuery.Spec.Reports {
		report, err := reportGetter.getReport(generationQuery.Namespace, reportName)
		if err != nil {
			return nil, err
		}
		reports[i] = report
	}
	return reports, nil
}

func ValidateReportGenerationQueryInputs(generationQuery *metering.ReportGenerationQuery, inputs []metering.ReportGenerationQueryInputValue) (map[string]interface{}, error) {
	var givenInputs, missingInputs, expectedInputs []string
	reportQueryInputs := make(map[string]interface{})
	inputDefinitions := make(map[string]metering.ReportGenerationQueryInputDefinition)

	for _, inputDef := range generationQuery.Spec.Inputs {
		inputDefinitions[inputDef.Name] = inputDef
	}

	for _, inputVal := range inputs {
		inputDef := inputDefinitions[inputVal.Name]
		val, err := convertQueryInputValueFromDefinition(inputVal, inputDef)
		if err != nil {
			return nil, err
		}
		reportQueryInputs[inputVal.Name] = val
		givenInputs = append(givenInputs, inputVal.Name)
	}

	// now validate the inputs match what the query is expecting
	for _, input := range generationQuery.Spec.Inputs {
		expectedInputs = append(expectedInputs, input.Name)
		// If the input isn't required than don't include it in the missing
		if !input.Required {
			continue
		}
		if _, ok := reportQueryInputs[input.Name]; !ok {
			missingInputs = append(missingInputs, input.Name)
		}
	}

	if len(missingInputs) != 0 {
		sort.Strings(expectedInputs)
		sort.Strings(givenInputs)
		return nil, fmt.Errorf("unable to validate ReportGenerationQuery %s inputs: requires %s as inputs, got %s", generationQuery.Name, strings.Join(expectedInputs, ","), strings.Join(givenInputs, ","))
	}

	return reportQueryInputs, nil
}

func convertQueryInputValueFromDefinition(inputVal metering.ReportGenerationQueryInputValue, inputDef metering.ReportGenerationQueryInputDefinition) (interface{}, error) {
	if inputVal.Value == nil {
		return nil, nil
	}

	inputType := strings.ToLower(inputDef.Type)
	if inputVal.Name == ReportingStartInputName || inputVal.Name == ReportingEndInputName {
		inputType = "time"
	}
	// unmarshal the data based on the input definition type
	var dst interface{}
	switch inputType {
	case "", "string":
		dst = new(string)
	case "time":
		dst = new(time.Time)
	case "int", "integer":
		dst = new(int)
	default:
		return nil, fmt.Errorf("unsupported input type %s", inputType)
	}
	err := json.Unmarshal(*inputVal.Value, dst)
	if err != nil {
		return nil, fmt.Errorf("inputs Name: %s is not valid a %s: value: %s, err: %s", inputVal.Name, inputType, string(*inputVal.Value), err)
	}
	return dst, nil
}
