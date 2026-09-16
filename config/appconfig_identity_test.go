package config

import "testing"

func TestResolveAppConfigCoord(t *testing.T) {
	sg, err := resolveAppConfigCoord("modelhub", "SG", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if sg.Application != "modelhub" || sg.Environment != "dev" || sg.Profile != "config-dev" {
		t.Fatalf("sg=%+v", sg)
	}
	us, err := resolveAppConfigCoord("modelhub", "US", "ppe_exhibition")
	if err != nil {
		t.Fatal(err)
	}
	if us.Application != "modelhub" || us.Environment != "ppe_exhibition" || us.Profile != "config-ppe_exhibition" {
		t.Fatalf("us=%+v", us)
	}
	if _, err := resolveAppConfigCoord("wghub", "SG", "dev"); err == nil {
		t.Fatal("foreign service must fail")
	}
	if _, err := resolveAppConfigCoord("modelhub", "CN", "dev"); err == nil {
		t.Fatal("CN must not select AppConfig")
	}
}
