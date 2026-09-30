## [unreleased]

## [1.2.0] - 2026-09-30

### 🚀 Features

- Improve UX and refacto some code for mobile usage
- Document the notification implementation usage
- Add label support and simplify vue on mobile
- Add admin panel control
- Add the possibility to sort lists by prices, name or labels
- Add more filters and fix updates on PWA
- Add new filters like store, tags
- Add trigger to notify tasks schedule
- *(auth)* Permit password auth disabling if OIDC is enabled and configured
- *(chore)* Add backup with webDAV system
- *(list-view)* Update budget summary calculation for active filters
- *(ui)* Replace fixed icon list with customizable emoji picker
- *(ui)* Disable select mode when unselect last item selected
- *(lists)* Improve recurring tasks configuration
- *(chore)* Add vibration on notifications with configuration in parameter

### 🐛 Bug Fixes

- Fix latest tag for docker image only for release tags
- Fix item bar width on mobile
- Change the way price badge on dashboard is calculated to fix it
- Fix multi user spaces which show unwanted items or lists
- Delete quantity for notes
- Change the minified view to replace link button by price view
- Fix issues with differents offline and online display
- Fix menu not showing on mobile display & change UX for the lists parameters
- Add placeholder on custom reminder
- *(ci)* Change icon texts to fix linting ci
- *(ui)* Fix mobile gesture on vanadium android

### 💼 Other

- Move language and theme selection in parameters
- Change button on mobile for simplicity and overlay issues
- *(claude.md)* Refacto the CLAUDE.md file to optimize the token usage

## [1.1.0] - 2026-09-02

### 🚀 Features

- Save last activity page to restore session
- Add possibility to pin shared lists in the main board
- Add the possibility to rename houses
- Add some options about the links entered to share, copy or open
- Add gesture for easy use on mobile
- Update trakka from the security audit
- Add possibility to reorganise the lists
- Implement push notifications when other users add items in shared lists
- Add price limits alerts

### 🐛 Bug Fixes

- Fix zooming textbox when selecting on mobile
- Change the counter to take only remaining items
- Improve reaction time of trakka when not used and reopen

### 📚 Documentation

- Add installation doc for Android and iOS
- Add QA doc for price limit tracker

## [1.0.1] - 2026-08-29

### 🐛 Bug Fixes

- Fix quantity calculation not working
- Fix header position for tiny screen like smartphones
- Fix menu too large with no horizontal scroll
- Fix menu for items in lists
- Fix UX design for items because the navigation were not easily usable
- Fix the scraper because some links doesn't show image or price
- Fix the price scraper because some prices are not showing

## [1.0.0] - 2026-08-28

### 🚀 Features

- Add reccurence for tasks
- Add Urgent items
- Track price for object with link to search if item exist with lower price exist
- Add admin panel
- Add light and dark mode
- Harden trakka with distroless image and some others tricks
- Create new list type to personalize lists if wanted
- Add customs categories or groups
- Add categories and spaces organisation. Add the possibility to edit the listes already created and add them to spaces
- Add possibility to share lists or spaces
- Improve the future updates for more stability
- Add github workflows and templates
- Add secret scan with gitleaks

### 🐛 Bug Fixes

- Fix continuity of the apps when client or server is disconnected
- Fix placeholder for month not showing
- Take the recurring shopping price in account for futur projection
- Fix the displayed informations not showing when server is disconnected
