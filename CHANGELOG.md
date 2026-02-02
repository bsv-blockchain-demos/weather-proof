# CHANGELOG

All notable changes to this project will be documented in this file. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Table of Contents

- [Unreleased](#unreleased)
- [1.0.0 - YYYY-MM-DD](#100---yyyy-mm-dd)

## [Unreleased]

### Added
- React frontend application for browsing and verifying weather records
- Weather list page with pagination and status filtering
- Weather detail page showing all 33 weather data fields
- Client-side blockchain verification using BEEF proofs and @bsv/sdk
- Confirmation status detection (Pending Confirmation vs On Chain)
- VerificationBadge component with visual status indicators
- Frontend Docker container with nginx serving
- Frontend integrated into docker-compose.yaml
- Comprehensive README.md with full-stack documentation
- Frontend-specific README.md in frontend/ directory

### Changed
- Updated QUICKSTART.md with frontend setup instructions
- Updated DOCKER_QUICKSTART.md with frontend service details
- Status filter dropdown now remains visible when no records match filter

### Fixed
- Weather list filter dropdown now shows even when "No weather records found"
- Verify button only appears for transactions confirmed on-chain (with merkle path)

---

## [1.0.0] - YYYY-MM-DD

### Added
- Initial release

---

### Template for New Releases:

Replace `X.X.X` with the new version number and `YYYY-MM-DD` with the release date:

```
## [X.X.X] - YYYY-MM-DD

### Added
- 

### Changed
- 

### Deprecated
- 

### Removed
- 

### Fixed
- 

### Security
- 
```

Use this template as the starting point for each new version. Always update the "Unreleased" section with changes as they're implemented, and then move them under the new version header when that version is released.