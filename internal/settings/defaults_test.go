package settings

import (
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
)

func TestPickDefaultsSingleAOSCluster(t *testing.T) {
	cls := []nutanix.Cluster{
		{ExtID: "pc", Name: "cluster-pc", IsPrismCentral: true},
		{ExtID: "cl-1", Name: "cluster-1", IsAOS: true},
	}
	cl, _ := PickDefaults(cls, nil)
	if cl != "cl-1" {
		t.Fatalf("cluster = %q", cl)
	}
}

func TestPickDefaultsSeveralClustersFirstByName(t *testing.T) {
	cls := []nutanix.Cluster{
		{ExtID: "cl-2", Name: "cluster-2", IsAOS: true},
		{ExtID: "cl-1", Name: "cluster-1", IsAOS: true},
	}
	cl, _ := PickDefaults(cls, nil)
	if cl != "cl-1" {
		t.Fatalf("cluster = %q", cl)
	}
}

func TestPickDefaultsPrefersDefaultContainer(t *testing.T) {
	cls := []nutanix.Cluster{{ExtID: "cl-1", Name: "cluster-1", IsAOS: true}}
	scs := []nutanix.StorageContainer{
		{ExtID: "big", Name: "big", ClusterExtID: "cl-1", FreeBytes: 9000},
		{ExtID: "def", Name: "default-container-123", ClusterExtID: "cl-1", FreeBytes: 10},
		{ExtID: "ss", Name: "SelfServiceContainer", ClusterExtID: "cl-1", IsSystem: true, FreeBytes: 99999},
	}
	if _, sc := PickDefaults(cls, scs); sc != "def" {
		t.Fatalf("container = %q", sc)
	}
}

func TestPickDefaultsSkipsObjectsAndInternal(t *testing.T) {
	cls := []nutanix.Cluster{{ExtID: "cl-1", Name: "cluster-1", IsAOS: true}}
	scs := []nutanix.StorageContainer{
		{ExtID: "obj", Name: "objectsl889411dccd9a44125aa3e31c2596dcf9", ClusterExtID: "cl-1", IsObjects: true, FreeBytes: 9000},
		{ExtID: "int", Name: "NutanixMetadataContainer", ClusterExtID: "cl-1", IsInternal: true, IsSystem: true, FreeBytes: 8000},
		{ExtID: "data", Name: "data", ClusterExtID: "cl-1", FreeBytes: 100},
	}
	if _, sc := PickDefaults(cls, scs); sc != "data" {
		t.Fatalf("container = %q", sc)
	}
}

func TestPickDefaultsMostFreeThenName(t *testing.T) {
	cls := []nutanix.Cluster{{ExtID: "cl-1", Name: "cluster-1", IsAOS: true}}
	scs := []nutanix.StorageContainer{
		{ExtID: "b", Name: "bravo", ClusterExtID: "cl-1", FreeBytes: 50},
		{ExtID: "c", Name: "charlie", ClusterExtID: "cl-1", FreeBytes: 70},
		{ExtID: "a", Name: "alpha", ClusterExtID: "cl-1", FreeBytes: 70},
	}
	if _, sc := PickDefaults(cls, scs); sc != "a" {
		t.Fatalf("container = %q", sc)
	}
}

func TestPickDefaultsOnlyOnThatCluster(t *testing.T) {
	cls := []nutanix.Cluster{
		{ExtID: "cl-1", Name: "cluster-1", IsAOS: true},
		{ExtID: "cl-2", Name: "cluster-2", IsAOS: true},
	}
	scs := []nutanix.StorageContainer{{ExtID: "other", Name: "default-container-9", ClusterExtID: "cl-2"}}
	cl, sc := PickDefaults(cls, scs)
	if cl != "cl-1" || sc != "" {
		t.Fatalf("picked %q %q", cl, sc)
	}
}

func TestPickDefaultsNothingEligible(t *testing.T) {
	cls := []nutanix.Cluster{{ExtID: "pc", Name: "cluster-pc", IsPrismCentral: true}}
	cl, sc := PickDefaults(cls, nil)
	if cl != "" || sc != "" {
		t.Fatalf("picked %q %q", cl, sc)
	}
}
