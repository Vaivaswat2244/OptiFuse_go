package enricher

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	pb "github.com/Vaivaswat2244/OptiFuse_go/proto"
)

// Enricher fetches CloudWatch telemetry and fills in the telemetry
// fields on each FunctionNode in the graph.
// Python equivalent: fetch_live_xray_data() in simulation/connectors/aws.py
type Enricher struct{}

// Enrich takes a Graph and AWS credentials, queries CloudWatch Logs Insights,
// and returns the same Graph with telemetry fields populated.
func (e *Enricher) Enrich(ctx context.Context, graph *pb.Graph, roleARN, externalID, serviceName, stage string) (*pb.Graph, []string, []string, error) {
	// Step 1: Assume the user's IAM role.
	// Python: get_assumed_role_session(user_role_arn, external_id)
	cfg, err := assumeRole(ctx, roleARN, externalID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("assume role: %w", err)
	}

	// Step 2: Build log group names from function IDs.
	// Pattern: /aws/lambda/{service_name}-{stage}-{function_id}
	// Python: log_group_names = [f"/aws/lambda/{service_name}-{stage}-{name}" for name in function_ids]
	functionIDs := make([]string, 0, len(graph.Nodes))
	for id := range graph.Nodes {
		functionIDs = append(functionIDs, id)
	}

	logGroups := make([]string, 0, len(functionIDs))
	for _, id := range functionIDs {
		logGroups = append(logGroups, fmt.Sprintf("/aws/lambda/%s-%s-%s", serviceName, stage, id))
	}

	// Step 3: Query CloudWatch Logs Insights.
	metrics, err := queryCloudWatch(ctx, cfg, logGroups, functionIDs, serviceName, stage)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cloudwatch query: %w", err)
	}

	// Step 4: Enrich graph nodes with the fetched metrics.
	// Python: ApplicationBuilder.enrich_with_live_data(base_application, live_metrics)
	enrichedIDs := make([]string, 0)
	missingIDs := make([]string, 0)

	for _, id := range functionIDs {
		if m, ok := metrics[id]; ok {
			if node, ok := graph.Nodes[id]; ok {
				node.AvgDurationMs = m.avgDurationMs
				node.AvgMemoryUsedMb = m.avgMemoryUsedMB
				node.InvocationCount = m.invocationCount
				node.P99LatencyMs = m.p99DurationMs
				node.AvgInitDurationMs = m.avgInitDurationMs
				node.P99InitDurationMs = m.p99InitDurationMs
				if m.invocationCount > 0 {
					node.ColdStartRate = float64(m.coldStarts) / float64(m.invocationCount)
				}
			}
			enrichedIDs = append(enrichedIDs, id)
		} else {
			missingIDs = append(missingIDs, id)
		}
	}

	return graph, enrichedIDs, missingIDs, nil
}

// lookbackWindow is how far back the Logs Insights query reaches.
//
// This was 24 hours, which silently returns nothing for a low-traffic
// application: an app invoked a dozen times a day may have no REPORT records at
// all in the last day, and the enricher then reports every function as missing
// and falls back to the YAML estimates. That looks identical to a permissions
// failure, and low-traffic apps are exactly the ones worth optimising.
//
// Cold starts make it worse. They are rare by nature, so a 24-hour window can
// contain invocations but no init records, leaving init duration unmeasurable
// on an app that is being used.
//
// Seven days by default, overridable with CLOUDWATCH_LOOKBACK_HOURS.
func lookbackWindow() time.Duration {
	const defaultHours = 24 * 7
	if v := os.Getenv("CLOUDWATCH_LOOKBACK_HOURS"); v != "" {
		if h, err := strconv.Atoi(v); err == nil && h > 0 {
			return time.Duration(h) * time.Hour
		}
	}
	return defaultHours * time.Hour
}

// functionMetrics holds the CloudWatch data for a single function.
type functionMetrics struct {
	avgDurationMs   float64
	p99DurationMs   float64
	avgMemoryUsedMB float64
	invocationCount int64

	// Cold start telemetry. coldStarts counts REPORT records carrying an
	// @initDuration field, which Lambda emits only when it had to build a new
	// execution environment.
	avgInitDurationMs float64
	p99InitDurationMs float64
	coldStarts        int64
}

// assumeRole assumes the user's IAM role and returns an AWS config.
// Python: get_assumed_role_session(user_role_arn, external_id)
func assumeRole(ctx context.Context, roleARN, externalID string) (aws.Config, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load default config: %w", err)
	}

	stsClient := sts.NewFromConfig(cfg)
	provider := stscreds.NewAssumeRoleProvider(stsClient, roleARN, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = "OptifuseAnalysisSession"
		if externalID != "" {
			o.ExternalID = aws.String(externalID)
		}
	})

	cfg.Credentials = aws.NewCredentialsCache(provider)
	return cfg, nil
}

// queryCloudWatch runs a Logs Insights query across all function log groups
// and returns per-function metrics.
// Python: fetch_live_xray_data() in simulation/connectors/aws.py
func queryCloudWatch(ctx context.Context, cfg aws.Config, logGroups, functionIDs []string, serviceName, stage string) (map[string]functionMetrics, error) {
	client := cloudwatchlogs.NewFromConfig(cfg)

	end := time.Now()
	start := end.Add(-lookbackWindow())

	// REPORT records are emitted for every single invocation and are never
	// sampled, unlike X-Ray traces (whose default rule keeps one request per
	// second plus 5% of the rest). That matters most for init duration: fitting
	// it from a sampled subset of cold starts would be fitting it from whichever
	// handful happened to survive sampling.
	//
	// @initDuration is present ONLY on cold starts, which is what makes
	// ispresent() a cold start counter. The average is computed as sum/count
	// rather than avg(@initDuration) because it is not worth depending on how
	// Logs Insights treats a field absent from most rows: sum/sum is correct
	// under either interpretation.
	query := `filter @type = "REPORT"
| stats avg(@duration) as avgDurationMS,
        pct(@duration, 99) as p99DurationMS,
        avg(@maxMemoryUsed) / 1024 / 1024 as avgMemoryMB,
        count(*) as invocations,
        sum(@initDuration) as totalInitMS,
        sum(ispresent(@initDuration)) as coldStarts,
        pct(@initDuration, 99) as p99InitMS
by @log as logGroupName`

	startResp, err := client.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupNames: logGroups,
		StartTime:     aws.Int64(start.Unix()),
		EndTime:       aws.Int64(end.Unix()),
		QueryString:   aws.String(query),
		Limit:         aws.Int32(10000),
	})
	if err != nil {
		// Gracefully handle missing log groups — same as Python version.
		if strings.Contains(err.Error(), "ResourceNotFoundException") {
			return map[string]functionMetrics{}, nil
		}
		return nil, fmt.Errorf("start query: %w", err)
	}

	// Poll until complete — Python used a while loop with time.sleep(1).
	//
	// Results are kept as rows. An earlier version flattened them into one slice
	// and re-chunked by a hardcoded stride of 4, which silently corrupted every
	// parse the moment the query gained or lost a field.
	var results [][]types.ResultField
	for {
		resp, err := client.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{
			QueryId: startResp.QueryId,
		})
		if err != nil {
			return nil, fmt.Errorf("get query results: %w", err)
		}
		if resp.Status == types.QueryStatusComplete {
			results = append(results, resp.Results...)
			break
		}
		if resp.Status == types.QueryStatusFailed || resp.Status == types.QueryStatusCancelled {
			return nil, fmt.Errorf("cloudwatch query failed with status: %s", resp.Status)
		}
		time.Sleep(1 * time.Second)
	}

	// Parse results — match log group name back to function ID.
	// Python: for result in query_results: matched_function_id = ...
	metrics := make(map[string]functionMetrics)
	for _, row := range results {
		logGroupName := fieldValue(row, "logGroupName")
		if logGroupName == "" {
			continue
		}

		// Match log group back to function ID.
		// /aws/lambda/optifuse-image-processing-test-dev-upload → upload
		var matchedID string
		for _, id := range functionIDs {
			suffix := fmt.Sprintf("-%s-%s-%s", serviceName, stage, id)
			if strings.HasSuffix(logGroupName, suffix) || strings.HasSuffix(logGroupName, "-"+id) {
				matchedID = id
				break
			}
		}
		if matchedID == "" {
			continue
		}

		var m functionMetrics
		var totalInitMs float64
		fmt.Sscanf(fieldValue(row, "avgDurationMS"), "%f", &m.avgDurationMs)
		fmt.Sscanf(fieldValue(row, "p99DurationMS"), "%f", &m.p99DurationMs)
		fmt.Sscanf(fieldValue(row, "avgMemoryMB"), "%f", &m.avgMemoryUsedMB)
		fmt.Sscanf(fieldValue(row, "invocations"), "%d", &m.invocationCount)
		fmt.Sscanf(fieldValue(row, "totalInitMS"), "%f", &totalInitMs)
		fmt.Sscanf(fieldValue(row, "coldStarts"), "%d", &m.coldStarts)
		fmt.Sscanf(fieldValue(row, "p99InitMS"), "%f", &m.p99InitDurationMs)

		// Averaged over cold starts only, since warm invocations have no init
		// phase at all. Dividing by total invocations would understate it by
		// exactly the cold start rate.
		if m.coldStarts > 0 {
			m.avgInitDurationMs = totalInitMs / float64(m.coldStarts)
		}
		metrics[matchedID] = m
	}

	return metrics, nil
}

// fieldValue finds a field value by name in a CloudWatch result row.
func fieldValue(row []types.ResultField, name string) string {
	for _, f := range row {
		if aws.ToString(f.Field) == name {
			return aws.ToString(f.Value)
		}
	}
	return ""
}
