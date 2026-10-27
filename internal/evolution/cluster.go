package evolution

import "sort"

// CauseCount is the number of failures that share a layer and a cause.
type CauseCount struct {
	Layer string `json:"layer"`
	Cause string `json:"cause"`
	Count int    `json:"count"`
}

// CountByCause aggregates attributed failures by layer and cause.
func CountByCause(failures []CaseOutcome) []CauseCount {
	type key struct{ layer, cause string }
	grouped := map[key]int{}
	var order []key
	for _, failure := range failures {
		if !failure.Failed {
			continue
		}
		k := key{failure.Layer, failure.Cause}
		if _, ok := grouped[k]; !ok {
			order = append(order, k)
		}
		grouped[k]++
	}
	out := make([]CauseCount, 0, len(order))
	for _, k := range order {
		out = append(out, CauseCount{Layer: k.layer, Cause: k.cause, Count: grouped[k]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Layer != out[j].Layer {
			return out[i].Layer < out[j].Layer
		}
		return out[i].Cause < out[j].Cause
	})
	return out
}

// Cluster groups failure samples that share a layer, cause, case type, and source.
type Cluster struct {
	Layer     string   `json:"layer"`
	Cause     string   `json:"cause"`
	CaseType  string   `json:"case_type"`
	SourceDoc string   `json:"source_doc"`
	Count     int      `json:"count"`
	CaseIDs   []string `json:"case_ids"`
	Signals   []string `json:"signals"`
}

// ClusterFailures groups attributed failures. The order is count, then layer,
// cause, type, and source, so the same input always yields the same clusters.
func ClusterFailures(failures []CaseOutcome) []Cluster {
	type key struct {
		layer, cause, caseType, source string
	}
	grouped := map[key]*Cluster{}
	var order []key
	for _, failure := range failures {
		if !failure.Failed {
			continue
		}
		k := key{failure.Layer, failure.Cause, failure.Type, failure.ExpectedSource}
		cluster, ok := grouped[k]
		if !ok {
			cluster = &Cluster{
				Layer:     failure.Layer,
				Cause:     failure.Cause,
				CaseType:  failure.Type,
				SourceDoc: failure.ExpectedSource,
				Signals:   append([]string(nil), failure.Signals...),
			}
			grouped[k] = cluster
			order = append(order, k)
		}
		cluster.Count++
		cluster.CaseIDs = append(cluster.CaseIDs, failure.ID)
	}
	out := make([]Cluster, 0, len(order))
	for _, k := range order {
		cluster := *grouped[k]
		sort.Strings(cluster.CaseIDs)
		if len(cluster.Signals) > 4 {
			cluster.Signals = cluster.Signals[:4]
		}
		out = append(out, cluster)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Layer != out[j].Layer {
			return out[i].Layer < out[j].Layer
		}
		if out[i].Cause != out[j].Cause {
			return out[i].Cause < out[j].Cause
		}
		if out[i].CaseType != out[j].CaseType {
			return out[i].CaseType < out[j].CaseType
		}
		return out[i].SourceDoc < out[j].SourceDoc
	})
	return out
}
