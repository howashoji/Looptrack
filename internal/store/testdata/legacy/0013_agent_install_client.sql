ALTER TABLE agent_installs
  ADD COLUMN client_os   VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'looptrack の GOOS（linux / darwin / windows）。空は Python の CLI',
  ADD COLUMN client_arch VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'looptrack の GOARCH（amd64 / arm64）';
