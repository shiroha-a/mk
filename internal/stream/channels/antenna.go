package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
)

// AntennaOwnerLookup resolves an antenna by id for the channel-side ownership
// gate. repository.AntennaRepository satisfies it (FindByID).
type AntennaOwnerLookup interface {
	FindByID(id string) (*model.Antenna, error)
}

// AntennaFactory builds AntennaChannels carrying an ownership lookup so Init can
// reject subscriptions to antennas the connecting user does not own (#1569).
type AntennaFactory struct {
	owners AntennaOwnerLookup
}

// NewAntennaFactory constructs an AntennaFactory. owners is used by Init to
// verify the antenna belongs to the connecting user. A nil owners fails closed
// (every subscription rejected).
func NewAntennaFactory(owners AntennaOwnerLookup) *AntennaFactory {
	return &AntennaFactory{owners: owners}
}

// New implements stream.ChannelFactory.
func (f *AntennaFactory) New(ctx stream.ChannelContext) stream.Channel {
	return &AntennaChannel{ctx: ctx, owners: f.owners}
}

// AntennaChannel forwards notes captured by a user-defined antenna.
type AntennaChannel struct {
	ctx    stream.ChannelContext
	owners AntennaOwnerLookup
	topic  string
}

func (c *AntennaChannel) Init(params json.RawMessage) error {
	// 認証必須
	user, ok := c.ctx.User().(*model.User)
	if !ok || user == nil {
		return stream.ErrInvalidParams
	}
	var p struct {
		AntennaID string `json:"antennaId"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	if p.AntennaID == "" {
		return stream.ErrInvalidParams
	}
	// Ownership gate (#1569): REST antennas/notes (Service.Show) と同じく、接続中の
	// user が所有する antenna でなければ購読を拒否する。antenna stream は push 段で
	// owner 可視性 (matchNote -> CanSeeNote, #1464) でのみ gate されるため、これが
	// 無いと任意の認証 user が他人の antennaTimeline:<id> を購読し、owner には
	// 見えるが自分には見えない followers / specified note を top-level で受信できる
	// (cross-user IDOR)。本家 AntennaChannel.init() の antenna.userId === user.id
	// 検証に相当。owners 未配線時は fail-closed で購読拒否 (#1444 / #1460 と同方針)。
	if c.owners == nil {
		return stream.ErrInvalidParams
	}
	a, err := c.owners.FindByID(p.AntennaID)
	if err != nil || a == nil || a.UserID != user.ID {
		return stream.ErrInvalidParams
	}
	c.topic = "antennaTimeline:" + p.AntennaID
	c.ctx.Subscribe(c.topic)
	return nil
}

func (c *AntennaChannel) OnRedisEvent(payload []byte) {
	payload = hideEmbeds(c.ctx, payload)
	_ = c.ctx.Send("note", json.RawMessage(payload))
}

func (c *AntennaChannel) OnClientMessage(string, json.RawMessage) {}

// RequiredPermission implements stream.PermittedChannel.
func (c *AntennaChannel) RequiredPermission() string { return "read:account" }

func (c *AntennaChannel) Dispose() {
	if c.topic != "" {
		c.ctx.Unsubscribe(c.topic)
	}
}
