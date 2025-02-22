package main

import (
	"testing"
)

func Test_getValuesFiles(t *testing.T) {
	type args struct {
		r *release
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "test case 1",
			args: args{
				r: &release{
					Name:        "release1",
					Description: "",
					Namespace:   "namespace",
					Enabled:     true,
					Chart:       "repo/chartX",
					Version:     "1.0",
					ValuesFile:  "test_files/values.yaml",
					Purge:       true,
					Test:        true,
				},
				//s: st,
			},
			want: " -f test_files/values.yaml",
		},
		{
			name: "test case 2",
			args: args{
				r: &release{
					Name:        "release1",
					Description: "",
					Namespace:   "namespace",
					Enabled:     true,
					Chart:       "repo/chartX",
					Version:     "1.0",
					ValuesFiles: []string{"test_files/values.yaml"},
					Purge:       true,
					Test:        true,
				},
				//s: st,
			},
			want: " -f test_files/values.yaml",
		},
		{
			name: "test case 1",
			args: args{
				r: &release{
					Name:        "release1",
					Description: "",
					Namespace:   "namespace",
					Enabled:     true,
					Chart:       "repo/chartX",
					Version:     "1.0",
					ValuesFiles: []string{"test_files/values.yaml", "test_files/values2.yaml"},
					Purge:       true,
					Test:        true,
				},
				//s: st,
			},
			want: " -f test_files/values.yaml -f test_files/values2.yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getValuesFiles(tt.args.r); got != tt.want {
				t.Errorf("getValuesFiles() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_inspectUpgradeScenario(t *testing.T){
	type args struct {
		r *release
		s releaseState
	}
	tests := []struct {
		name       string
		args       args
		want       decisionType
	}{
		{
			name:       "inspectUpgradeScenario() - local chart with different chart name should change",
			args: args{
				r: &release{
					Name:      "release1",
					Namespace: "namespace",
					Version: "1.0.0",
					Chart: "/local/charts",
					Enabled:   true,
				},
				s: releaseState{
					Namespace: "namespace",
					Chart: "chart-1.0.0",
				},
			},
			want: change,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome = plan{}

			// Act
			inspectUpgradeScenario(tt.args.r, tt.args.s)
			got := outcome.Decisions[0].Type
			t.Log(outcome.Decisions[0].Description)

			// Assert
			if got != tt.want {
				t.Errorf("decide() = %s, want %s", got, tt.want)
			}
		})
	}
}

func Test_decide(t *testing.T) {
	type args struct {
		r *release
		s *state
	}
	tests := []struct {
		name       string
		targetFlag []string
		args       args
		want       decisionType
	}{
		{
			name:       "decide() - targetMap does not contain this service - skip",
			targetFlag: []string{"someOtherRelease"},
			args: args{
				r: &release{
					Name:      "release1",
					Namespace: "namespace",
					Enabled:   true,
				},
				s: &state{},
			},
			want: noop,
		},
		{
			name:       "decide() - targetMap does not contain this service - skip",
			targetFlag: []string{"someOtherRelease", "norThisOne"},
			args: args{
				r: &release{
					Name:      "release1",
					Namespace: "namespace",
					Enabled:   true,
				},
				s: &state{},
			},
			want: noop,
		},
		{
			name:       "decide() - targetMap is empty - will install",
			targetFlag: []string{},
			args: args{
				r: &release{
					Name:      "release4",
					Namespace: "namespace",
					Enabled:   true,
				},
				s: &state{},
			},
			want: create,
		},
		{
			name:       "decide() - targetMap is exactly this service - will install",
			targetFlag: []string{"thisRelease"},
			args: args{
				r: &release{
					Name:      "thisRelease",
					Namespace: "namespace",
					Enabled:   true,
				},
				s: &state{},
			},
			want: create,
		},
		{
			name:       "decide() - targetMap contains this service - will install",
			targetFlag: []string{"notThisOne", "thisRelease"},
			args: args{
				r: &release{
					Name:      "thisRelease",
					Namespace: "namespace",
					Enabled:   true,
				},
				s: &state{},
			},
			want: create,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targetMap = make(map[string]bool)

			for _, target := range tt.targetFlag {
				targetMap[target] = true
			}
			outcome = plan{}

			// Act
			decide(tt.args.r, tt.args.s)
			got := outcome.Decisions[0].Type
			t.Log(outcome.Decisions[0].Description)

			// Assert
			if got != tt.want {
				t.Errorf("decide() = %s, want %s", got, tt.want)
			}
		})
	}
}

// String allows for pretty printing decisionType const
func (dt decisionType) String() string {
	switch dt {
	case create:
		return "create"
	case change:
		return "change"
	case delete:
		return "delete"
	case noop:
		return "noop"
	}
	return "unknown"
}
