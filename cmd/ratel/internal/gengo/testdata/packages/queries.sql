-- name: ProfileOf :one
SELECT * FROM people_profiles WHERE user_id = @user_id;

-- The group "queries" shares the package of the settings model.
-- name: SettingOf :one
SELECT * FROM settings WHERE key = @key;
