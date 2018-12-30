var bidSocket
var sessID = ""

function loaded(){
    bidSocket = new WebSocket("ws://localhost:8080/ws")
    var stat = document.getElementById("status-div");
    var longstat = document.getElementById("long-status-div");

    var update = function(){
        bidSocket.onmessage = function (event) {
            msg = JSON.parse(event.data)
            stat.textContent = msg.action
            if (msg.action == "error"){
                longstat.textContent = msg.value
            } else {
                longstat.textContent = ""
            }
            console.log(msg.action)
            console.log(msg.value)
        }
      };
      window.setTimeout(update);

}

function sendPnr(nr){
    console.log(nr)
    bidSocket.send(JSON.stringify({action:"pnrAuth", value:nr, id:sessID}))
}