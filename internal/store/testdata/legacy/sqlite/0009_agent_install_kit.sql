ALTER TABLE agent_installs ADD COLUMN core_bundle_sha256 TEXT NULL;
ALTER TABLE agent_installs ADD COLUMN loop_state TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_installs ADD COLUMN loop_bundle_sha256 TEXT NULL;
ALTER TABLE agent_installs ADD COLUMN loop_version TEXT NOT NULL DEFAULT '';
