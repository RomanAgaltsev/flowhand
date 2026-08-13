-- +goose Up
ALTER TABLE tasks ADD COLUMN handler TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ALTER COLUMN handler DROP DEFAULT;

-- +goose Down
ALTER TABLE tasks DROP COLUMN handler;
