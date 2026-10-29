-- name: Purge :exec
DELETE FROM app.events;

-- name: EventsAfter :many
SELECT id FROM app.events WHERE note = $1 AND id > $2;
