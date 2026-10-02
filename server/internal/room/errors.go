package room

// Error is returned to clients as {"error": Code}; the frontend translates the code.
type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string { return e.Code }

var (
	ErrNotFound         = &Error{404, "not_found"}
	ErrSongNotFound     = &Error{404, "song_not_found"}
	ErrUnauthorized     = &Error{401, "unauthorized"}
	ErrForbidden        = &Error{403, "forbidden"}
	ErrNotMember        = &Error{403, "not_member"}
	ErrBanned           = &Error{403, "banned"}
	ErrInvalid          = &Error{400, "invalid"}
	ErrNicknameInvalid  = &Error{400, "nickname_invalid"}
	ErrNameInvalid      = &Error{400, "name_invalid"}
	ErrRoomExists       = &Error{409, "room_exists"}
	ErrNicknameTaken    = &Error{409, "nickname_taken"}
	ErrDuplicateSong    = &Error{409, "duplicate_song"}
	ErrQueueLimit       = &Error{409, "queue_limit"}
	ErrNotCurrent       = &Error{409, "not_current"}
	ErrReorderNeedsFIFO = &Error{409, "reorder_requires_fifo"}
	ErrNoOriginal       = &Error{409, "no_original"}
	ErrQueueNotEmpty    = &Error{409, "queue_not_empty"}
	ErrNoHistory        = &Error{409, "no_history"}
	ErrPlayerExists     = &Error{409, "player_exists"}
	ErrPairCode         = &Error{404, "pair_code_invalid"}
	ErrInternal         = &Error{500, "internal"}
)
