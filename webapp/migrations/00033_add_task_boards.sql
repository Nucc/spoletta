-- Task boards: columns of shared structs' records, each column a struct
-- and a query of its records, owned as flows are. See taskboards.go.

-- +goose Up
CREATE TABLE task_boards (
  id bigint NOT NULL AUTO_INCREMENT,
  name varchar(255) NOT NULL,
  owner_user_id bigint DEFAULT NULL,
  owner_team_id bigint DEFAULT NULL,
  created_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at timestamp(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY task_boards_owner_user (owner_user_id),
  KEY task_boards_owner_team (owner_team_id),
  CONSTRAINT task_boards_owner_user FOREIGN KEY (owner_user_id) REFERENCES users (id),
  CONSTRAINT task_boards_owner_team FOREIGN KEY (owner_team_id) REFERENCES teams (id)
);

-- A column's struct is NULL once the struct is deleted for good.
CREATE TABLE task_board_columns (
  id bigint NOT NULL AUTO_INCREMENT,
  board_id bigint NOT NULL,
  position int NOT NULL,
  name varchar(255) NOT NULL,
  struct_id bigint DEFAULT NULL,
  query text NOT NULL,
  sort varchar(255) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY task_board_columns_board (board_id, position),
  KEY task_board_columns_struct (struct_id),
  CONSTRAINT task_board_columns_board FOREIGN KEY (board_id) REFERENCES task_boards (id) ON DELETE CASCADE,
  CONSTRAINT task_board_columns_struct FOREIGN KEY (struct_id) REFERENCES flows (id) ON DELETE SET NULL
);

-- +goose Down
-- The task boards and their columns are lost.
DROP TABLE task_board_columns;
DROP TABLE task_boards;
