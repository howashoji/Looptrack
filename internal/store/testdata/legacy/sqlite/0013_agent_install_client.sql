ALTER TABLE agent_installs ADD COLUMN client_os TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_installs ADD COLUMN client_arch TEXT NOT NULL DEFAULT '';
