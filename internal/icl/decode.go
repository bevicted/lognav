package icl

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/logging"
)

// DecodeStreamItem extracts logs, warnings, and errors from one decoded stream
// item. Pure: no SDK/uv types. The caller (ui/msgs.streamCallback) stamps
// instance/queryID onto the resulting LogStreamMsg. A per-result decode error
// is appended to errs and decoding continues (one bad record never drops the
// batch). A nil item (no event payload) yields empty slices.
func DecodeStreamItem(item *StreamItem) (logs []Log, warns, errs []string) {
	if item == nil {
		return nil, nil, nil
	}
	if item.Error != nil {
		errs = append(errs, deref(item.Error.Message))
	}
	for _, apiErr := range item.Errors {
		errs = append(errs, fmt.Sprintf("%s\n\n%s\n\n%s",
			deref(apiErr.Code), deref(apiErr.Message), deref(apiErr.MoreInfo)))
	}
	if item.Warning != nil && item.Warning.TimeRangeWarning != nil {
		warns = append(warns, deref(item.Warning.TimeRangeWarning.WarningMessage))
	}
	if item.Result != nil {
		for _, result := range item.Result.Results {
			l, err := newLogResp(result)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			logs = append(logs, l)
		}
	}
	return logs, warns, errs
}

// newLogResp makes a new Log msg by extracting all data from the dataprime result.
func newLogResp(result DataprimeResults) (Log, error) {
	if result.UserData == nil {
		return Log{}, errors.New("nil UserData field in log result")
	}
	data := map[string]any{}
	if err := jsonutil.API.Unmarshal([]byte(*result.UserData), &data); err != nil {
		return Log{}, err
	}
	md, err := newMetadata(result.Metadata)
	if err != nil {
		return Log{}, err
	}
	return Log{
		Data: map[string]any{
			"data":     data,
			"labels":   unpackResults(result.Labels),
			"metadata": unpackResults(result.Metadata),
		},
		Metadata: md,
	}, nil
}

func newMetadata(metadataResp []KeyValue) (Metadata, error) {
	m := Metadata{}
	logger := slog.Default().With(logging.KeyComponent, "logstream")

	for _, field := range metadataResp {
		if field.Key == nil || field.Value == nil {
			logger.Debug("nil field in metadata results")
			continue
		}
		switch *field.Key {
		case "logid":
			m.ID = *field.Value
		case "priorityclass":
			m.Priority = PriorityFromString(*field.Value)
		case "severity":
			m.Severity = SeverityFromString(*field.Value)
		case "timestampMicros":
			i, err := strconv.ParseInt(*field.Value, 10, 64)
			if err != nil {
				return Metadata{}, fmt.Errorf("parse timestampMicros: %w", err)
			}
			m.TSMicro = i
		}
	}

	return m, nil
}

func unpackResults(results []KeyValue) map[string]any {
	m := make(map[string]any, len(results))
	for _, result := range results {
		if result.Key == nil || result.Value == nil {
			continue
		}
		m[*result.Key] = *result.Value
	}
	return m
}
