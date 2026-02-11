cmake_minimum_required(VERSION 3.16)
project(magnetk_transport C)

set(CMAKE_C_STANDARD 11)

if(WIN32)
    set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -O2")
endif()

# ===== MsQuic paths =====
# MsQuic is pre-built in the msquic/ directory at project root
set(MSQUIC_ROOT "${CMAKE_CURRENT_SOURCE_DIR}/../msquic")
set(MSQUIC_INCLUDE_DIR "${MSQUIC_ROOT}/src/inc")

# Platform-specific lib path
if(WIN32)
    set(MSQUIC_LIB_DIR "${MSQUIC_ROOT}/artifacts/bin/windows/x64_Debug_schannel")
    set(MSQUIC_LIB "${MSQUIC_LIB_DIR}/msquic.lib")
elseif(APPLE)
    set(MSQUIC_LIB_DIR "${MSQUIC_ROOT}/artifacts/bin/macos/x64_Debug_openssl")
    set(MSQUIC_LIB "${MSQUIC_LIB_DIR}/libmsquic.dylib")
else()
    set(MSQUIC_LIB_DIR "${MSQUIC_ROOT}/artifacts/bin/linux/x64_Debug_openssl")
    set(MSQUIC_LIB "${MSQUIC_LIB_DIR}/libmsquic.so")
endif()

# ===== Transport Library =====
add_library(magnetk_transport STATIC
    src/transport.c
)

target_include_directories(magnetk_transport PUBLIC
    ${CMAKE_CURRENT_SOURCE_DIR}/include
    ${MSQUIC_INCLUDE_DIR}
)

target_link_libraries(magnetk_transport PUBLIC
    ${MSQUIC_LIB}
)

if(WIN32)
    target_link_libraries(magnetk_transport PRIVATE ws2_32 crypt32 ncrypt)
    target_compile_definitions(magnetk_transport PRIVATE MAGNETK_TRANSPORT_EXPORTS)
else()
    target_link_libraries(magnetk_transport PRIVATE pthread)
endif()

# Also build a shared library for SDKs
add_library(magnetk_transport_shared SHARED
    src/transport.c
)

target_include_directories(magnetk_transport_shared PUBLIC
    ${CMAKE_CURRENT_SOURCE_DIR}/include
    ${MSQUIC_INCLUDE_DIR}
)

target_link_libraries(magnetk_transport_shared PUBLIC
    ${MSQUIC_LIB}
)

if(WIN32)
    target_link_libraries(magnetk_transport_shared PUBLIC ws2_32 crypt32 ncrypt)
    target_compile_definitions(magnetk_transport_shared PRIVATE MAGNETK_TRANSPORT_EXPORTS)
else()
    target_link_libraries(magnetk_transport_shared PUBLIC pthread)
endif()

set_target_properties(magnetk_transport_shared PROPERTIES
    OUTPUT_NAME magnetk_transport
)

# ===== Copy MsQuic DLL to output =====
if(WIN32)
    add_custom_command(TARGET magnetk_transport_shared POST_BUILD
        COMMAND ${CMAKE_COMMAND} -E copy_if_different
            "${MSQUIC_LIB_DIR}/msquic.dll"
            "$<TARGET_FILE_DIR:magnetk_transport_shared>/msquic.dll"
        COMMENT "Copying msquic.dll to output directory"
    )
endif()

# ===== Install =====
install(TARGETS magnetk_transport magnetk_transport_shared
    ARCHIVE DESTINATION lib
    LIBRARY DESTINATION lib
    RUNTIME DESTINATION bin
)
install(FILES include/transport.h DESTINATION include)

message(STATUS "MsQuic include: ${MSQUIC_INCLUDE_DIR}")
message(STATUS "MsQuic lib: ${MSQUIC_LIB}")
