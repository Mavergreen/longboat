cmake_minimum_required(VERSION 3.16)
set(LONGBOAT_ARCH "x86_64" CACHE STRING "which slice of the universal updater this configure builds: x86_64 | arm64")
if(LONGBOAT_ARCH STREQUAL "x86_64")
  set(CMAKE_OSX_DEPLOYMENT_TARGET 10.9 CACHE STRING "")
  set(CMAKE_OSX_ARCHITECTURES x86_64 CACHE STRING "")
elseif(LONGBOAT_ARCH STREQUAL "arm64")
  set(CMAKE_OSX_DEPLOYMENT_TARGET 11.0 CACHE STRING "")
  set(CMAKE_OSX_ARCHITECTURES arm64 CACHE STRING "")
else()
  message(FATAL_ERROR "LONGBOAT_ARCH must be 'x86_64' or 'arm64' (got '${LONGBOAT_ARCH}')")
endif()
project(LongboatUpdater LANGUAGES OBJC)
find_package(MavericksShipyard REQUIRED)
include(Mavericks)
include(MavericksSparkle)
mavericks_resolve_version(MAV_LONGBOAT_VERSION)
mavericks_add_updater_app(
  PRODUCT longboat
  PRODUCT_NAME "Mavericks Longboat"
  VERSION "${MAV_LONGBOAT_VERSION}"
  LOG_PREFIX "MavLongboatUpdater"
  CONFIRM_TITLE "Mavericks Longboat updated"
  CONFIRM_BODY "Longboat for Mavericks in /usr/local/mavergreen/longboat was updated."
  ALLOW_GENERIC
)
