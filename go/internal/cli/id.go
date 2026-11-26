package cli

import (
	"errors"
	"fmt"

	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/magnetk"
)

// IdMain implements "magnetk id": show this device's identity key and the
// magnetk:// address others use to reach it with proof of who it is.
func IdMain(args []string) error {
	nf := newNodeFlags("id")
	if err := nf.parse(args); err != nil {
		return err
	}
	cfg, err := nf.nodeConfig([]string{"<relay>"})
	if err != nil {
		return err
	}
	if cfg.Key == nil {
		return errors.New("identity is off (IDENTITY_FILE=off)")
	}
	fp := cfg.Key.Fingerprint()
	id := cfg.ID
	if id == "" {
		id = defaultChatID(nf)
	}
	fmt.Printf("fingerprint  %s\n", identity.Format(fp))
	fmt.Printf("node id      %s (relays bind it to this key)\n", id)
	fmt.Printf("address      %s\n", (&magnetk.Address{Node: id, Relays: cfg.Relays[:1], Key: fp}).String())
	fmt.Println("\nGive the address to people who should reach you: whoever opens it checks")
	fmt.Println("that messages really come from this key, so nobody can pretend to be you.")
	return nil
}
