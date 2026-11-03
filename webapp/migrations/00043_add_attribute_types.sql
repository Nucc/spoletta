-- An attribute can have a type, what each of its values must be: text, an
-- integer, a number, a yes or no, or a date and time, none for any JSON
-- value; and text a pattern, a regular expression the whole of it must
-- match. See attribute_types.go.

-- +goose Up
ALTER TABLE object_attributes ADD COLUMN value_type varchar(16) NOT NULL DEFAULT '';
ALTER TABLE object_attributes ADD COLUMN value_pattern varchar(1000) NOT NULL DEFAULT '';

-- +goose Down
-- The attributes' types and patterns are lost; the attributes and the
-- values runs stored stay.
ALTER TABLE object_attributes DROP COLUMN value_pattern;
ALTER TABLE object_attributes DROP COLUMN value_type;
