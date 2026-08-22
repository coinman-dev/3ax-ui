package tunnel

import "github.com/coinman-dev/3ax-ui/v2/database/model"

// Server and Client are the database models of a merged tunnel. They were
// separate neutral structs while AmneziaWG and WireGuard still lived in four
// tables; now that both share tunnel_servers/tunnel_clients, the model itself
// is the flavour-neutral view and the conversion bridges are gone.
type (
	Server = model.TunnelServer
	Client = model.TunnelClient
)
