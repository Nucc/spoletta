-- What a task board's cards show of each record is the board's, for each
-- struct its columns hold, rather than each column's: a JSON object of the
-- struct's id to what its cards show, the field heading them as the ID and
-- the one it links to among them; NULL for what they show unless told. Each
-- board takes what the first of its columns of a struct showing something
-- told shows. See taskboards.go.

-- +goose Up
ALTER TABLE task_boards ADD COLUMN cards json DEFAULT NULL;

UPDATE task_boards b JOIN (
    SELECT c.board_id, JSON_OBJECTAGG(CAST(c.struct_id AS CHAR), c.card) AS cards
    FROM task_board_columns c
    WHERE c.card IS NOT NULL AND c.struct_id IS NOT NULL AND c.position = (
        SELECT MIN(f.position) FROM task_board_columns f
        WHERE f.board_id = c.board_id AND f.struct_id = c.struct_id AND f.card IS NOT NULL)
    GROUP BY c.board_id
) told ON told.board_id = b.id
SET b.cards = told.cards;

ALTER TABLE task_board_columns DROP COLUMN card;

-- +goose Down
-- Each column of a struct takes back what the board's cards of it show. An
-- older webapp reads neither the field picked as the ID nor the one it
-- links to, and drops them as it saves the board.
ALTER TABLE task_board_columns ADD COLUMN card json DEFAULT NULL;

UPDATE task_board_columns c JOIN task_boards b ON b.id = c.board_id
SET c.card = JSON_EXTRACT(b.cards, CONCAT('$."', c.struct_id, '"'))
WHERE b.cards IS NOT NULL AND c.struct_id IS NOT NULL;

ALTER TABLE task_boards DROP COLUMN cards;
