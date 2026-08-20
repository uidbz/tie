"""Error types for the tie client."""


class TieError(Exception):
    """Base class for every tie client error."""


class Unauthorized(TieError):
    """Raised when the server rejects credentials (HTTP 401)."""


class NotFound(TieError):
    """Raised when a key has no associated values.

    Mirrors the Go client's ErrNotFound sentinel: the server reports
    "Key has no associated values" with Success=false, which is a normal
    empty-result signal rather than a transport failure.
    """


class ServerError(TieError):
    """Raised when the server returns Success=false with a real message."""
