package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	SelectorStrategyWarm      = "warm"
	SelectorStrategyPin       = "pin"
	SelectorStrategySpillover = "spillover"
	SelectorStrategyFallback  = "fallback"
)

// selectorStrategyList is the human readable list of valid strategies used in
// validation errors.
const selectorStrategyList = "warm, pin, spillover, fallback"

// FallbackSettings configures the fallback strategy.
type FallbackSettings struct {
	// Group names the routing group that is watched for occupancy. While
	// another member of that group is starting or ready, the selector skips the
	// primary target and serves the request from the first usable fallback
	// target. Leave it empty to use the group that contains the primary target.
	Group string `yaml:"group" json:"group"`
}

// SelectorSettings contains strategy-specific selector settings.
type SelectorSettings struct {
	Spillover int              `yaml:"spillover" json:"spillover"`
	Fallback  FallbackSettings `yaml:"fallback" json:"fallback"`
}

// SelectorConfig describes a virtual model ID that resolves to a concrete
// local model, alias, or peer model for each request.
type SelectorConfig struct {
	Strategy    string           `yaml:"strategy" json:"strategy"`
	Targets     []string         `yaml:"targets" json:"targets"`
	Settings    SelectorSettings `yaml:"settings" json:"settings"`
	Name        string           `yaml:"name" json:"name"`
	Description string           `yaml:"description" json:"description"`
	Unlisted    bool             `yaml:"unlisted" json:"unlisted"`
	Metadata    map[string]any   `yaml:"metadata" json:"metadata"`
}

// UnmarshalYAML applies selector defaults while retaining the distinction
// between an omitted spillover value and an explicitly invalid zero.
func (c *SelectorConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawSelectorConfig SelectorConfig
	defaults := rawSelectorConfig{
		Targets:  []string{},
		Settings: SelectorSettings{Spillover: 1},
		Metadata: map[string]any{},
	}
	if err := value.Decode(&defaults); err != nil {
		return err
	}
	*c = SelectorConfig(defaults)
	return nil
}

func validateSelectors(config Config) error {
	for selectorID, selector := range config.Selectors {
		if strings.TrimSpace(selectorID) == "" {
			return fmt.Errorf("selectors: selector names cannot be empty")
		}
		if _, found := config.Models[selectorID]; found {
			return fmt.Errorf("selectors.%s: name conflicts with model ID %q", selectorID, selectorID)
		}
		if _, found := config.aliases[selectorID]; found {
			return fmt.Errorf("selectors.%s: name conflicts with model alias %q", selectorID, selectorID)
		}

		switch selector.Strategy {
		case SelectorStrategyPin, SelectorStrategyWarm, SelectorStrategySpillover, SelectorStrategyFallback:
		case "":
			return fmt.Errorf("selectors.%s.strategy is required", selectorID)
		default:
			return fmt.Errorf("selectors.%s.strategy: unknown strategy %q (valid: %s)", selectorID, selector.Strategy, selectorStrategyList)
		}
		if len(selector.Targets) == 0 {
			return fmt.Errorf("selectors.%s.targets must contain at least one entry", selectorID)
		}

		resolvedTargets := make([]string, 0, len(selector.Targets))
		localTargets := make([]string, 0, len(selector.Targets))
		for i, target := range selector.Targets {
			if _, found := config.Selectors[target]; found {
				return fmt.Errorf("selectors.%s.targets[%d] references selector %q; selector chaining is not supported", selectorID, i, target)
			}
			if _, found := config.ResolveBaseModel(target); !found {
				return fmt.Errorf("selectors.%s.targets[%d] references unknown model %q", selectorID, i, target)
			}

			realName, local := config.RealModelName(target)
			if selector.Strategy == SelectorStrategyWarm {
				if !local {
					return fmt.Errorf("selectors.%s.targets[%d] must resolve to a local model for strategy %q", selectorID, i, selector.Strategy)
				}
			}
			if selector.Strategy == SelectorStrategySpillover {
				if local {
					localTargets = append(localTargets, realName)
				} else {
					peerID, modelID, _ := config.ResolvePeerModel(target)
					realName = PeerModelFQN(peerID, modelID)
				}
				resolvedTargets = append(resolvedTargets, realName)
			}
		}

		if selector.Strategy == SelectorStrategyFallback {
			if err := validateFallbackSelector(config, selectorID, selector); err != nil {
				return err
			}
			continue
		}

		if selector.Strategy != SelectorStrategySpillover {
			continue
		}
		if selector.Settings.Spillover < 1 {
			return fmt.Errorf("selectors.%s.settings.spillover must be >= 1", selectorID)
		}

		seen := make(map[string]struct{}, len(resolvedTargets))
		for _, target := range resolvedTargets {
			if _, duplicate := seen[target]; duplicate {
				return fmt.Errorf("selectors.%s.targets contains duplicate resolved model %q", selectorID, target)
			}
			seen[target] = struct{}{}
		}
		if err := validateSpilloverCoexistence(config, selectorID, localTargets); err != nil {
			return err
		}
	}
	return nil
}

// FallbackGroup resolves the routing group that a fallback selector watches and
// returns its ID together with its member model IDs. An empty
// settings.fallback.group resolves to the group that contains the primary
// target.
func (config Config) FallbackGroup(selector SelectorConfig) (string, []string, error) {
	if config.Routing.Router.Use == "matrix" {
		return "", nil, fmt.Errorf("strategy %q requires the group router, the matrix router has no groups", SelectorStrategyFallback)
	}

	groupID := strings.TrimSpace(selector.Settings.Fallback.Group)
	if groupID == "" {
		if len(selector.Targets) == 0 {
			return "", nil, fmt.Errorf("strategy %q requires at least one target", SelectorStrategyFallback)
		}
		primary, local := config.RealModelName(selector.Targets[0])
		if !local {
			return "", nil, fmt.Errorf("strategy %q needs settings.fallback.group when the primary target %q is not a local model", SelectorStrategyFallback, selector.Targets[0])
		}
		for id, group := range config.Routing.Router.Settings.Groups {
			for _, member := range group.Members {
				if member == primary {
					groupID = id
					break
				}
			}
			if groupID != "" {
				break
			}
		}
		if groupID == "" {
			return "", nil, fmt.Errorf("strategy %q: primary target %q is not in a routing group, set settings.fallback.group", SelectorStrategyFallback, selector.Targets[0])
		}
	}

	group, found := config.Routing.Router.Settings.Groups[groupID]
	if !found {
		return "", nil, fmt.Errorf("settings.fallback.group %q is not a routing group", groupID)
	}
	return groupID, group.Members, nil
}

func validateFallbackSelector(config Config, selectorID string, selector SelectorConfig) error {
	if len(selector.Targets) < 2 {
		return fmt.Errorf("selectors.%s.targets must contain a primary target and at least one fallback target for strategy %q", selectorID, SelectorStrategyFallback)
	}
	if _, _, err := config.FallbackGroup(selector); err != nil {
		return fmt.Errorf("selectors.%s: %w", selectorID, err)
	}
	return nil
}

func validateSpilloverCoexistence(config Config, selectorID string, targets []string) error {
	if len(targets) <= 1 {
		return nil
	}

	if config.Routing.Router.Use == "matrix" {
		matrix := config.Routing.Router.Settings.Matrix
		if matrix != nil && matrix.program != nil && matrix.program.CanContainAll(targets) {
			return nil
		}
		return fmt.Errorf("selectors.%s.targets must all appear together in one expanded matrix set", selectorID)
	}

	groupOf := make(map[string]string, len(config.Models))
	for groupID, group := range config.Routing.Router.Settings.Groups {
		for _, member := range group.Members {
			groupOf[member] = groupID
		}
	}

	groupID := groupOf[targets[0]]
	if groupID == "" {
		return fmt.Errorf("selectors.%s target %q is not in a routing group", selectorID, targets[0])
	}
	if config.Routing.Router.Settings.Groups[groupID].Swap {
		return fmt.Errorf("selectors.%s spillover targets must share a group with swap: false", selectorID)
	}
	for _, target := range targets[1:] {
		if groupOf[target] != groupID {
			return fmt.Errorf("selectors.%s spillover targets must share one routing group", selectorID)
		}
	}
	return nil
}
