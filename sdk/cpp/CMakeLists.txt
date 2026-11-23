cmake_minimum_required(VERSION 3.16)
project(magnetk_cpp_sdk CXX)

# The SDK is header-only and loads libmagnetk at runtime, so nothing needs to
# be linked except the platform's dynamic loader and threads.
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

find_package(Threads REQUIRED)

add_library(magnetk INTERFACE)
target_include_directories(magnetk INTERFACE ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_link_libraries(magnetk INTERFACE Threads::Threads ${CMAKE_DL_LIBS})

option(MAGNETK_BUILD_EXAMPLES "Build examples" ON)
option(MAGNETK_BUILD_TESTS "Build tests" ON)

# All executables go to one directory (also for multi-config generators such
# as Visual Studio), next to the runtime libraries staged below.
set(MAGNETK_BIN_DIR "${CMAKE_BINARY_DIR}/bin")
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY "${MAGNETK_BIN_DIR}")
foreach(cfg Debug Release RelWithDebInfo MinSizeRel)
    string(TOUPPER ${cfg} CFG)
    set(CMAKE_RUNTIME_OUTPUT_DIRECTORY_${CFG} "${MAGNETK_BIN_DIR}")
endforeach()

# Where libmagnetk (+ msquic) live; staged next to the executables.
set(MAGNETK_LIB_DIR "${CMAKE_CURRENT_SOURCE_DIR}/../../build/lib" CACHE PATH "Directory with libmagnetk and msquic")

set(_stage_cmds "")
if(EXISTS "${MAGNETK_LIB_DIR}")
    list(APPEND _stage_cmds COMMAND ${CMAKE_COMMAND} -E copy_directory "${MAGNETK_LIB_DIR}" "${MAGNETK_BIN_DIR}")
else()
    message(WARNING "MAGNETK_LIB_DIR (${MAGNETK_LIB_DIR}) not found: build libmagnetk first (scripts/build) or set MAGNETK_LIB at run time")
endif()
if(MINGW)
    # MinGW C++ runtime, so the programs run outside the MSYS2 shell
    get_filename_component(_mingw_bin "${CMAKE_CXX_COMPILER}" DIRECTORY)
    foreach(_dll libstdc++-6.dll libgcc_s_seh-1.dll libwinpthread-1.dll)
        if(EXISTS "${_mingw_bin}/${_dll}")
            list(APPEND _stage_cmds COMMAND ${CMAKE_COMMAND} -E copy_if_different "${_mingw_bin}/${_dll}" "${MAGNETK_BIN_DIR}")
        endif()
    endforeach()
endif()
add_custom_target(magnetk_runtime ALL
    COMMAND ${CMAKE_COMMAND} -E make_directory "${MAGNETK_BIN_DIR}"
    ${_stage_cmds}
    COMMENT "Staging libmagnetk and runtime libraries in ${MAGNETK_BIN_DIR}")

function(magnetk_exe name src)
    add_executable(${name} ${src})
    target_link_libraries(${name} PRIVATE magnetk)
    add_dependencies(${name} magnetk_runtime)
endfunction()

if(MAGNETK_BUILD_EXAMPLES)
    magnetk_exe(chat examples/chat.cpp)
    magnetk_exe(share examples/share.cpp)
    magnetk_exe(download examples/download.cpp)
    magnetk_exe(rpc examples/rpc.cpp)
endif()

if(MAGNETK_BUILD_TESTS)
    enable_testing()
    magnetk_exe(test_sdk test/test_sdk.cpp)
    add_test(NAME magnetk_sdk COMMAND test_sdk WORKING_DIRECTORY "${MAGNETK_BIN_DIR}")
endif()

install(FILES include/magnetk.hpp DESTINATION include)
install(FILES ../c/include/magnetk.h DESTINATION include)
