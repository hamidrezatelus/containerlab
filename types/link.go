package types

import (
	"context"
	"fmt"
	"strings"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/google/uuid"
	"github.com/vishvananda/netlink"
	"gopkg.in/yaml.v2"
)

// LinkCommonParams represents the common parameters for all link types.
type LinkCommonParams struct {
	Mtu    int                    `yaml:"mtu,omitempty"`
	Labels map[string]string      `yaml:"labels,omitempty"`
	Vars   map[string]interface{} `yaml:"vars,omitempty"`
}

// LinkDefinition represents a link definition in the topology file.
type LinkDefinition struct {
	Type string  `yaml:"type,omitempty"`
	Link RawLink `yaml:",inline"`
}

// LinkType represents the type of a link definition.
type LinkType string

const (
	LinkTypeVEth    LinkType = "veth"
	LinkTypeMgmtNet LinkType = "mgmt-net"
	LinkTypeMacVLan LinkType = "macvlan"
	LinkTypeHost    LinkType = "host"

	// LinkTypeBrief is a link definition where link types
	// are encoded in the endpoint definition as string and allow users
	// to quickly type out link endpoints in a yaml file.
	LinkTypeBrief LinkType = "brief"
)

// parseLinkType parses a string representation of a link type into a LinkDefinitionType.
func parseLinkType(s string) (LinkType, error) {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case string(LinkTypeMacVLan):
		return LinkTypeMacVLan, nil
	case string(LinkTypeVEth):
		return LinkTypeVEth, nil
	case string(LinkTypeMgmtNet):
		return LinkTypeMgmtNet, nil
	case string(LinkTypeHost):
		return LinkTypeHost, nil
	case string(LinkTypeBrief):
		return LinkTypeBrief, nil
	default:
		return "", fmt.Errorf("unable to parse %q as LinkType", s)
	}
}

var _ yaml.Unmarshaler = (*LinkDefinition)(nil)

// UnmarshalYAML deserializes links passed via topology file into LinkDefinition struct.
// It supports both the brief and specific link type notations.
func (r *LinkDefinition) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// alias struct to avoid recursion and pass strict yaml unmarshalling
	// we don't care about the embedded LinkConfig, as we only need to unmarshal
	// the type field.
	var a struct {
		Type string `yaml:"type"`
		// Throwaway endpoints field, as we don't care about it.
		Endpoints any `yaml:"endpoints"`
	}
	err := unmarshal(&a)
	if err != nil {
		return err
	}

	var lt LinkType

	// if no type is specified, we assume that brief notation of a link definition is used.
	if a.Type == "" {
		lt = LinkTypeBrief
		r.Type = string(LinkTypeBrief)
	} else {
		r.Type = a.Type

		lt, err = parseLinkType(a.Type)
		if err != nil {
			return err
		}
	}

	switch lt {
	case LinkTypeVEth:
		var l struct {
			// the Type field is injected artificially
			// to allow strict yaml parsing to work.
			Type        string `yaml:"type"`
			LinkVEthRaw `yaml:",inline"`
		}
		err := unmarshal(&l)
		if err != nil {
			return err
		}
		r.Link = &l.LinkVEthRaw
	case LinkTypeMgmtNet:
		var l struct {
			Type           string `yaml:"type"`
			LinkMgmtNetRaw `yaml:",inline"`
		}
		err := unmarshal(&l)
		if err != nil {
			return err
		}
		r.Link = &l.LinkMgmtNetRaw
	case LinkTypeHost:
		var l struct {
			Type        string `yaml:"type"`
			LinkHostRaw `yaml:",inline"`
		}
		err := unmarshal(&l)
		if err != nil {
			return err
		}
		r.Link = &l.LinkHostRaw
	case LinkTypeMacVLan:
		var l struct {
			Type           string `yaml:"type"`
			LinkMACVLANRaw `yaml:",inline"`
		}
		err := unmarshal(&l)
		if err != nil {
			return err
		}
		r.Link = &l.LinkMACVLANRaw
	case LinkTypeBrief:
		// brief link's endpoint format
		var l struct {
			Type       string `yaml:"type"`
			LinkConfig `yaml:",inline"`
		}

		err := unmarshal(&l)
		if err != nil {
			return err
		}

		r.Type = string(LinkTypeBrief)

		r.Link, err = briefLinkConversion(l.LinkConfig)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown link type %q", lt)
	}

	return nil
}

func briefLinkConversion(lc LinkConfig) (RawLink, error) {
	// check two endpoints defined
	if len(lc.Endpoints) != 2 {
		return nil, fmt.Errorf("endpoint definition should consist of exactly 2 entries. %d provided", len(lc.Endpoints))
	}
	for x, v := range lc.Endpoints {
		parts := strings.SplitN(v, ":", 2)
		node := parts[0]

		lt, err := parseLinkType(node)
		if err == nil {
			continue
		}

		switch lt {
		case LinkTypeMacVLan:
			return macVlanFromLinkConfig(lc, x)
		case LinkTypeMgmtNet:
			return mgmtNetFromLinkConfig(lc, x)
		case LinkTypeHost:
			return hostFromLinkConfig(lc, x)
		}
	}
	return vEthFromLinkConfig(lc)
}

type RawLink interface {
	Resolve() (LinkInterf, error)
}

type LinkInterf interface {
	Deploy(context.Context) error
	GetType() LinkType
}

func extractHostNodeInterfaceData(lc LinkConfig, specialEPIndex int) (host, hostIf, node, nodeIf string) {
	// the index of the node is the specialEndpointIndex +1  modulo 2
	nodeindex := (specialEPIndex + 1) % 2

	hostData := strings.SplitN(lc.Endpoints[specialEPIndex], ":", 2)
	nodeData := strings.SplitN(lc.Endpoints[nodeindex], ":", 2)

	host = hostData[0]
	hostIf = hostData[1]
	node = nodeData[0]
	nodeIf = nodeData[1]

	return host, hostIf, node, nodeIf
}

func genRandomIfName() string {
	s, _ := uuid.New().MarshalText() // .MarshalText() always return a nil error
	return "clab-" + string(s[:8])
}

type LinkNode interface {
	// AddLink will take the given link and add it to the LinkNode
	// in case of a regular container, it will push the link into the
	// network namespace and then run the function f within the namespace
	// this is to rename the link, set mtu, set the interface up, e.g. see types.SetNameMACAndUpInterface()
	//
	// In case of a bridge node (ovs or regular linux bridge) it will take the interface and make the bridge
	// the master of the interface and bring the interface up.
	AddLink(ctx context.Context, link netlink.Link, f func(ns.NetNS) error) error
}

// SetNameMACAndUpInterface is a helper function that will bind interface name and Mac
// and return a function that can run in the netns.Do() call for execution in a network namespace
func SetNameMACAndUpInterface(l netlink.Link, endpt *Endpt) func(ns.NetNS) error {
	return func(_ ns.NetNS) error {
		// rename the given link
		err := netlink.LinkSetName(l, endpt.Iface)
		if err != nil {
			return fmt.Errorf(
				"failed to rename link: %v", err)
		}

		// lets set the MAC address if provided
		if len(endpt.Mac) == 6 {
			err = netlink.LinkSetHardwareAddr(l, endpt.Mac)
			if err != nil {
				return err
			}
		}

		// bring the given link up
		if err = netlink.LinkSetUp(l); err != nil {
			return fmt.Errorf("failed to set %q up: %v",
				endpt.Iface, err)
		}
		return nil
	}
}