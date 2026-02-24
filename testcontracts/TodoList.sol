// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

contract TodoList {
    struct Task { uint256 id; string content; bool completed; }
    Task[] public tasks;
    event TaskCreated(uint256 id, string content, bool completed);
    event TaskCompleted(uint256 id, bool completed);

    function createTask(string calldata _content) external {
        uint256 taskId = tasks.length;
        tasks.push(Task(taskId, _content, false));
        emit TaskCreated(taskId, _content, false);
    }
    function toggleCompleted(uint256 _id) external {
        require(_id < tasks.length, "Task does not exist");
        Task storage task = tasks[_id];
        task.completed = !task.completed;
        emit TaskCompleted(_id, task.completed);
    }
    function getTask(uint256 _id) external view returns (uint256, string memory, bool) {
        require(_id < tasks.length, "Task does not exist");
        Task memory task = tasks[_id];
        return (task.id, task.content, task.completed);
    }
    function getTasksCount() external view returns (uint256) {
        return tasks.length;
    }
}
