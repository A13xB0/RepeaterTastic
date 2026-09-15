package config

import "testing"

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load("../../deploy/repeatertastic.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Links.MQTT) != 1 || c.Links.MQTT[0].Name != "public" {
		t.Fatalf("example mqtt = %+v", c.Links.MQTT)
	}
}
