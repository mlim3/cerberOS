package openbao

import (
	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/openbao/openbao/api/v2"
)

// KvMount is the KV v2 mount path (see vault/setup-openbao.sh: sys/mounts/kv).
const KvMount = "kv"

// OpenBaoSecretManager stores secrets in OpenBao KV v2.
type OpenBaoSecretManager struct {
	client *api.Client
	logger *audit.Logger
}
