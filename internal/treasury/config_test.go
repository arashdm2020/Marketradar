package treasury

import (
	"testing"
	"tradebot/internal/profile"
	"tradebot/internal/webapp"
)

func TestMainnetDefaultsSeedOnceAndPersist(t *testing.T) {
	p,e:=profile.Open(t.TempDir()+"/defaults.db");if e!=nil{t.Fatal(e)};defer p.DB.Close()
	if _,e=webapp.NewStore(p.DB);e!=nil{t.Fatal(e)}
	s:=&Store{DB:p.DB,Profiles:p}
	if e=EnsurePublicConfig(p.DB);e!=nil{t.Fatal(e)}
	if e=s.SeedDestination(DefaultDestination);e!=nil{t.Fatal(e)}
	c,e:=LoadPublicConfig(p.DB,nil);if e!=nil{t.Fatal(e)}
	if c.ChainID!=56||c.RPCURL!=DefaultMainnetRPC||c.Confirmations!=12||c.MonitoringSeconds!=15{t.Fatalf("unexpected defaults: %+v",c)}
	d,e:=s.ActiveDestination();if e!=nil||d.Address!="0x0f65096f6c02aa6dc0d0b8c99ddebbc3cd90b3f5"{t.Fatalf("destination: %+v %v",d,e)}
	if _,e=p.DB.Exec(`UPDATE treasury_public_config SET rpc_url='https://override.invalid',confirmations=20 WHERE id=1`);e!=nil{t.Fatal(e)}
	if e=EnsurePublicConfig(p.DB);e!=nil{t.Fatal(e)}
	c,e=LoadPublicConfig(p.DB,nil);if e!=nil{t.Fatal(e)}
	if c.RPCURL!="https://override.invalid"||c.Confirmations!=20{t.Fatalf("migration overwrote configuration: %+v",c)}
}
