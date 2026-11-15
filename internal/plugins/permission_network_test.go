package plugins

import (
	"database/sql"

	"context"
	"github.com/universaltill/universal-till/internal/data"
	"reflect"
	"testing"
)

// ut-docs#3137: a signer's network needs come from its granted permissions.
func TestPluginNetworkNeeds(t *testing.T) {
	cases := []struct {
		name     string
		perms    []string
		settings map[string]string
		want     NetworkNeeds
	}{
		{"network-free", []string{"events:receive", "storage"}, nil, NetworkNeeds{}},
		{"exact host", []string{"events:receive", "net:Signer-API.Example.com."}, nil,
			NetworkNeeds{Any: true, Hosts: []string{"signer-api.example.com"}}},
		{"two hosts", []string{"net:a.example.com", "net:b.example.com"}, nil,
			NetworkNeeds{Any: true, Hosts: []string{"a.example.com", "b.example.com"}}},
		{"wildcard", []string{"net:*"}, nil, NetworkNeeds{Any: true, Unprobeable: true}},
		{"account", []string{"net:{account}"}, nil, NetworkNeeds{Any: true, Unprobeable: true}},
		{"tcp", []string{"tcp:192.168.1.5:80"}, nil, NetworkNeeds{Any: true, Unprobeable: true}},
		// A setting URL may carry a port or http scheme the probe would not
		// use (review of ut-docs#3137, blocker 1): never probed.
		{"setting resolved", []string{"net:@setting:url"}, map[string]string{"url": "https://api.example.com/v2/x?token=1"},
			NetworkNeeds{Any: true, Unprobeable: true}},
		{"setting with port", []string{"net:@setting:url"}, map[string]string{"url": "https://tse.example.com:5618/api"},
			NetworkNeeds{Any: true, Unprobeable: true}},
		{"setting plain http", []string{"net:@setting:url"}, map[string]string{"url": "http://tse.example.com/api"},
			NetworkNeeds{Any: true, Unprobeable: true}},
		{"setting unset", []string{"net:@setting:url"}, nil, NetworkNeeds{Any: true, Unprobeable: true}},
		{"lan ip", []string{"net:192.168.1.50"}, nil, NetworkNeeds{Any: true, Unprobeable: true}},
		{"lan name", []string{"net:kasa"}, nil, NetworkNeeds{Any: true, Unprobeable: true}},
		// A dotted LAN name or a plain-http-capable plugin (blocker 2).
		{"mdns name", []string{"net:tse.local"}, nil, NetworkNeeds{Any: true, Unprobeable: true}},
		{"home.arpa name", []string{"net:tse.home.arpa"}, nil, NetworkNeeds{Any: true, Unprobeable: true}},
		{"http:lan", []string{"net:tse.example.com", "http:lan"}, nil,
			NetworkNeeds{Any: true, Unprobeable: true, Hosts: []string{"tse.example.com"}}},
		{"validation host", []string{"net:validation:check.example.com"}, nil, NetworkNeeds{Any: true, Unprobeable: true}},
		{"mixed", []string{"net:a.example.com", "net:*"}, nil,
			NetworkNeeds{Any: true, Unprobeable: true, Hosts: []string{"a.example.com"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := managerTestDB(t)
			seedInstalledPlugin(t, db, "com.test.nn", "nn", "1.0.0", "wasm", true)
			for _, p := range c.perms {
				grantPerm(t, db, "com.test.nn", p)
			}
			for k, v := range c.settings {
				setPluginSetting(t, db, "com.test.nn", k, v)
			}
			got, err := PluginNetworkNeeds(context.Background(), db, "com.test.nn")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestPluginNetworkNeeds_IgnoresUngrantedAndReportsErrors(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.test.nn", "nn", "1.0.0", "wasm", true)
	grantPerm(t, db, "com.test.nn", "net:a.example.com")
	if err := dataSetPerm(db, "com.test.nn", "net:a.example.com", false); err != nil {
		t.Fatal(err)
	}
	if got, err := PluginNetworkNeeds(context.Background(), db, "com.test.nn"); err != nil || got.Any {
		t.Fatalf("revoked grant: got %+v err %v, want network-free", got, err)
	}
	db.Close()
	if _, err := PluginNetworkNeeds(context.Background(), db, "com.test.nn"); err == nil {
		t.Fatal("closed DB must return an error")
	}
}

func dataSetPerm(db *sql.DB, id, perm string, granted bool) error {
	return data.NewPluginRepo(db).SetPermission(context.Background(), id, perm, granted)
}
