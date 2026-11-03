-- A struct's attribute can refer to another shared struct's records, holding
-- the unique value of the one it means, such as a pull request's ticket
-- (object_attributes.ref_id, the struct's object referred to). An object on
-- a board can follow such a reference from another object, standing for the
-- record it names (objects.follows); a run holds the record it followed as
-- it holds one it picked, in taken_records, marked followed, which keeps no
-- other run from it. See references.go.

-- +goose Up
ALTER TABLE object_attributes ADD COLUMN ref_id bigint DEFAULT NULL;
ALTER TABLE objects ADD COLUMN follows json DEFAULT NULL;
ALTER TABLE taken_records ADD COLUMN followed boolean NOT NULL DEFAULT FALSE;

-- +goose Down
-- Which attributes referred to which structs, which objects followed them
-- and which records runs followed are lost; the values, objects and runs
-- stay. A followed record left in taken_records would read as picked, so
-- those rows go.
DELETE FROM taken_records WHERE followed;
ALTER TABLE taken_records DROP COLUMN followed;
ALTER TABLE objects DROP COLUMN follows;
ALTER TABLE object_attributes DROP COLUMN ref_id;
