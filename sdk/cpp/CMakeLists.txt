cmake_minimum_required(VERSION 3.16)
project(magnetk_cpp_sdk CXX)

set(CMAKE_CXX_STANDARD 17)

# MsQuic and C Transport paths
set(MAGNET_C_DIR "${CMAKE_CURRENT_SOURCE_DIR}/../../c")
set(MSQUIC_ROOT "${CMAKE_CURRENT_SOURCE_DIR}/../../msquic")

# Add C Transport library
add_subdirectory("${MAGNET_C_DIR}" bin_c_transport)

# C++ SDK Library
add_library(magnetk_cpp STATIC
    src/magnetk_link.cpp
    src/client.cpp
    src/seed.cpp
)

target_include_directories(magnetk_cpp PUBLIC
    include
    ${MAGNET_C_DIR}/include
    ${MSQUIC_ROOT}/src/inc
)

target_link_libraries(magnetk_cpp PUBLIC
    magnetk_transport
)

# Test Executable
add_executable(test_sdk test/test_sdk.cpp)
target_link_libraries(test_sdk PRIVATE magnetk_cpp)

if(WIN32)
    # Ensure MsQuic DLL is copied
    add_custom_command(TARGET test_sdk POST_BUILD
        COMMAND ${CMAKE_COMMAND} -E copy_if_different
            "${MSQUIC_ROOT}/artifacts/bin/windows/x64_Debug_schannel/msquic.dll"
            "$<TARGET_FILE_DIR:test_sdk>/msquic.dll"
    )
endif()
