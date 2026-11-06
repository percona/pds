package everest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Scale and enable-schedule read an instance, edit it, and PUT the whole
// object back, so every spec member this package has no field for would come
// back absent and the API server would read that as a delete.
//
// The types that can carry members the adapter does not model keep the leftover
// JSON in an Unknown map and write it back out untouched.

// unknownFields is the undecoded remainder of one JSON object.
type unknownFields map[string]json.RawMessage

// splitUnknown decodes data into known, which must be a pointer to a struct,
// and returns the members of data that struct has no field for.
func splitUnknown(data []byte, known any) (unknownFields, error) {
	if err := json.Unmarshal(data, known); err != nil {
		return nil, err
	}

	rest := unknownFields{}
	if err := json.Unmarshal(data, &rest); err != nil {
		return nil, err
	}

	for name := range modeledNames(known) {
		delete(rest, name)
	}

	if len(rest) == 0 {
		return nil, nil
	}

	return rest, nil
}

// marshalWithUnknown encodes known and adds back the members it does not carry.
// A modeled field always wins, so an edit the adapter made cannot be undone by
// a stale copy of the same key.
func marshalWithUnknown(known any, unknown unknownFields) ([]byte, error) {
	encoded, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	if len(unknown) == 0 {
		return encoded, nil
	}

	merged := unknownFields{}
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}

	for name, value := range unknown {
		if _, modeled := merged[name]; modeled {
			continue
		}
		merged[name] = value
	}

	return json.Marshal(merged)
}

// modeledNames is the set of JSON member names a struct has a field for. It is
// read from the tags rather than listed by hand so adding a field cannot leave
// the same key in both the typed view and the Unknown map.
func modeledNames(known any) map[string]struct{} {
	t := reflect.Indirect(reflect.ValueOf(known)).Type()

	names := make(map[string]struct{}, t.NumField())
	for i := range t.NumField() {
		field := t.Field(i)

		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = field.Name
		}

		names[name] = struct{}{}
	}

	return names
}

// UnmarshalJSON keeps the spec members the adapter does not model, so a scale
// cannot delete members such as `parameters`.
func (s *InstanceSpec) UnmarshalJSON(data []byte) error {
	var fields instanceSpecFields

	unknown, err := splitUnknown(data, &fields)
	if err != nil {
		return fmt.Errorf("everest: decode instance spec: %w", err)
	}

	*s = InstanceSpec(fields)
	s.Unknown = unknown

	return nil
}

// MarshalJSON writes the typed fields and the kept remainder.
func (s InstanceSpec) MarshalJSON() ([]byte, error) {
	return marshalWithUnknown(instanceSpecFields(s), s.Unknown)
}

// UnmarshalJSON keeps the component members the adapter does not model, such
// as `affinity`.
func (c *ComponentSpec) UnmarshalJSON(data []byte) error {
	var fields componentSpecFields

	unknown, err := splitUnknown(data, &fields)
	if err != nil {
		return fmt.Errorf("everest: decode component: %w", err)
	}

	*c = ComponentSpec(fields)
	c.Unknown = unknown

	return nil
}

// MarshalJSON writes the typed fields and the kept remainder.
func (c ComponentSpec) MarshalJSON() ([]byte, error) {
	return marshalWithUnknown(componentSpecFields(c), c.Unknown)
}

// UnmarshalJSON keeps the source members the adapter does not model.
func (d *DataSource) UnmarshalJSON(data []byte) error {
	var fields dataSourceFields

	unknown, err := splitUnknown(data, &fields)
	if err != nil {
		return fmt.Errorf("everest: decode data source: %w", err)
	}

	*d = DataSource(fields)
	d.Unknown = unknown

	return nil
}

// MarshalJSON writes the typed fields and the kept remainder.
func (d DataSource) MarshalJSON() ([]byte, error) {
	return marshalWithUnknown(dataSourceFields(d), d.Unknown)
}

// UnmarshalJSON keeps the service members the adapter does not model, so a
// later provider field under `service` survives a scale.
func (s *ServiceSpec) UnmarshalJSON(data []byte) error {
	var fields serviceSpecFields

	unknown, err := splitUnknown(data, &fields)
	if err != nil {
		return fmt.Errorf("everest: decode service: %w", err)
	}

	*s = ServiceSpec(fields)
	s.Unknown = unknown

	return nil
}

// MarshalJSON writes the typed fields and the kept remainder.
func (s ServiceSpec) MarshalJSON() ([]byte, error) {
	return marshalWithUnknown(serviceSpecFields(s), s.Unknown)
}

// UnmarshalJSON keeps the load balancer members the adapter does not model.
func (l *LoadBalancerService) UnmarshalJSON(data []byte) error {
	var fields loadBalancerServiceFields

	unknown, err := splitUnknown(data, &fields)
	if err != nil {
		return fmt.Errorf("everest: decode load balancer service: %w", err)
	}

	*l = LoadBalancerService(fields)
	l.Unknown = unknown

	return nil
}

// MarshalJSON writes the typed fields and the kept remainder.
func (l LoadBalancerService) MarshalJSON() ([]byte, error) {
	return marshalWithUnknown(loadBalancerServiceFields(l), l.Unknown)
}

type (
	instanceSpecFields        InstanceSpec
	componentSpecFields       ComponentSpec
	dataSourceFields          DataSource
	serviceSpecFields         ServiceSpec
	loadBalancerServiceFields LoadBalancerService
)
