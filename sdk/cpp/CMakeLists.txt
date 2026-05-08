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

# Examples
add_executable(cpp_share examples/share.cpp)
target_link_libraries(cpp_share PRIVATE magnetk_cpp)

add_executable(cpp_download examples/download.cpp)
target_link_libraries(cpp_download PRIVATE magnetk_cpp)

if(WIN32)
    foreach(target test_sdk cpp_share cpp_download)
        add_custom_command(TARGET ${target} POST_BUILD
            COMMAND ${CMAKE_COMMAND} -E copy_if_different
                "$<TARGET_FILE:magnetk_transport_shared>"
                "$<TARGET_FILE_DIR:${target}>/magnetk_transport.dll"
            COMMAND ${CMAKE_COMMAND} -E copy_if_different
                "${MSQUIC_ROOT}/artifacts/bin/windows/x64_Debug_schannel/msquic.dll"
                "$<TARGET_FILE_DIR:${target}>/msquic.dll"
        )
    endforeach()
endif()
